// Echo AI (Local) — A voice echo bot using open-source STT/TTS.
//
// This demo answers incoming SIP calls and echoes back what the caller says,
// using faster-whisper for speech-to-text and Kokoro for text-to-speech.
// No cloud APIs or API keys required — everything runs locally in Docker.
//
// Architecture:
//
//	Caller → SIP PBX → xphone-go (this app)
//	                        │
//	                        ├── PCMReader ([]int16, 8kHz, 20ms frames)
//	                        │       ↓
//	                        │   Energy VAD (silence detection)
//	                        │       ↓ (on silence after speech)
//	                        │   WAV buffer → faster-whisper HTTP :8000
//	                        │       ↓
//	                        │   transcript text
//	                        │       ↓
//	                        │   Kokoro TTS HTTP :8880
//	                        │       ↓ (24kHz PCM → downsample to 8kHz)
//	                        └── PacedPCMWriter (plays audio back to caller)
//
// Key difference from the Deepgram version (echo-ai-cloud):
// STT is batch HTTP, not streaming WebSocket. We use energy-based silence
// detection (VAD) to segment speech into utterances, then send each complete
// utterance to Whisper as a WAV file.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/x-phone/xphone-go"
)

// VAD (Voice Activity Detection) tuning constants.
// These control how the app segments continuous audio into discrete utterances.
const (
	// speechThreshold is the RMS energy level above which audio is considered speech.
	// Typical quiet line noise is ~100-200, speech is ~1000-5000+.
	// Lower = more sensitive (picks up quieter speech but also more noise).
	speechThreshold = 500

	// silenceFrames is how many consecutive silent frames (each 20ms) trigger
	// the end of an utterance. 25 frames = 500ms of silence.
	silenceFrames = 25

	// minSpeechFrames is the minimum number of frames for a valid utterance.
	// Anything shorter is discarded (filters out clicks, pops, brief noise).
	// 10 frames = 200ms.
	minSpeechFrames = 10

	// maxSpeechDuration caps the buffer to prevent unbounded memory growth
	// if someone speaks continuously without pausing. 15000 frames = 5 minutes.
	maxSpeechDuration = 15000
)

func main() {
	// Load SIP credentials and service URLs from environment variables.
	sipUser := requireEnv("SIP_USERNAME")
	sipPass := requireEnv("SIP_PASSWORD")
	sipHost := requireEnv("SIP_HOST")
	sipTransport := envOr("SIP_TRANSPORT", "udp")
	sttURL := envOr("STT_URL", "http://localhost:8000")
	ttsURL := envOr("TTS_URL", "http://localhost:8880")

	cfg := &localConfig{sttURL: sttURL, ttsURL: ttsURL}

	// Create a new SIP phone. xphone handles SIP registration, call state
	// machines, RTP media, and codec encode/decode under the hood.
	phone := xphone.New(
		xphone.WithCredentials(sipUser, sipPass, sipHost),
		xphone.WithTransport(sipTransport, nil),
		xphone.WithCodecs(xphone.CodecPCMU, xphone.CodecPCMA),
	)

	// Handle incoming calls: auto-answer and start the echo loop.
	phone.OnIncoming(func(call xphone.Call) {
		log.Printf("Incoming call from %s", call.From())
		if err := call.Accept(); err != nil {
			log.Printf("Accept failed: %v", err)
			return
		}
		go handleCall(call, cfg)
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	phone.OnRegistered(func() {
		log.Println("Registered — waiting for calls")
	})

	// Connect to the SIP server and register our extension.
	log.Printf("Connecting to %s as %s (%s)...", sipHost, sipUser, sipTransport)
	if err := phone.Connect(ctx); err != nil {
		log.Fatalf("Connect failed: %v", err)
	}
	if phone.State() != xphone.PhoneStateRegistered {
		log.Fatalf("Registration failed — check credentials and SIP host")
	}

	// Block until Ctrl+C or SIGTERM.
	<-ctx.Done()
	log.Println("Shutting down...")
	phone.Disconnect()
}

type localConfig struct {
	sttURL string
	ttsURL string
}

// handleCall runs the echo loop for a single call.
//
// It sets up two concurrent pipelines:
//   - VAD goroutine: reads caller audio → detects speech → transcribes → sends to actions channel
//   - Main loop: reads actions channel → synthesizes speech → plays back to caller
//
// DTMF key presses also feed into the actions channel.
func handleCall(call xphone.Call, cfg *localConfig) {
	// actions channel carries text to be spoken back to the caller.
	// Both STT results ("You said: ...") and DTMF events ("You pressed: ...")
	// are sent here, and the main loop plays them in order.
	actions := make(chan string, 8)

	// done is closed when the call ends (remote hangup, timeout, etc.)
	// and signals all goroutines to exit.
	done := make(chan struct{})

	call.OnEnded(func(_ xphone.EndReason) {
		close(done)
	})

	// Forward DTMF key presses to the actions channel.
	call.OnDTMF(func(digit string) {
		select {
		case actions <- fmt.Sprintf("You pressed: %s", digit):
		default:
		}
	})

	// --- VAD + STT goroutine ---
	// Reads raw PCM audio from the caller, uses energy-based voice activity
	// detection to find speech segments, then sends each segment to Whisper
	// for transcription.
	go func() {
		// PCMReader returns a channel of []int16 frames (160 samples = 20ms @ 8kHz).
		pcmCh := call.PCMReader()

		// VAD state machine: silence → speech → trailing → (send to STT) → silence
		type vadState int
		const (
			stateSilence  vadState = iota // No speech detected
			stateSpeech                   // Active speech
			stateTrailing                 // Speech ended, counting silence frames
		)

		state := stateSilence
		var speechBuf []int16 // Accumulates PCM samples during speech
		silenceCount := 0     // Consecutive silent frames in trailing state

		for {
			select {
			case samples, ok := <-pcmCh:
				if !ok {
					return // Call audio stream closed
				}

				// Compute RMS energy for this 20ms frame to decide if it's speech.
				energy := rmsEnergy(samples)

				switch state {
				case stateSilence:
					// Waiting for speech to start.
					if energy > speechThreshold {
						state = stateSpeech
						speechBuf = append(speechBuf[:0], samples...)
						silenceCount = 0
					}

				case stateSpeech:
					// Actively recording speech.
					speechBuf = append(speechBuf, samples...)
					if energy < speechThreshold {
						// Speech may have ended — start counting silence.
						state = stateTrailing
						silenceCount = 1
					}
					if len(speechBuf)/160 > maxSpeechDuration {
						// Safety cap — prevent unbounded buffer growth.
						state = stateSilence
						speechBuf = speechBuf[:0]
					}

				case stateTrailing:
					// Speech paused — waiting to see if it resumes or stays silent.
					speechBuf = append(speechBuf, samples...)
					if energy > speechThreshold {
						// Speech resumed — keep recording.
						state = stateSpeech
						silenceCount = 0
					} else {
						silenceCount++
						if silenceCount >= silenceFrames {
							// Enough silence (500ms) — utterance is complete.
							// Only transcribe if the utterance was long enough
							// to be real speech (not just a click or pop).
							if len(speechBuf)/160 >= minSpeechFrames {
								text, err := transcribe(cfg.sttURL, speechBuf)
								if err != nil {
									log.Printf("STT failed: %v", err)
								} else if text != "" {
									select {
									case actions <- fmt.Sprintf("You said: %s", text):
									default:
									}
								}
							}
							state = stateSilence
							speechBuf = speechBuf[:0]
						}
					}
				}
			case <-done:
				return
			}
		}
	}()

	// Play a greeting when the call starts (using the "af_bella" voice).
	playTTS(call, cfg, "Hello! Say something and I'll repeat it back to you. You can also press any key.", "af_bella")

	// Main loop: pull actions (transcriptions or DTMF events) and echo them
	// back to the caller using TTS (using the "af_sky" voice).
	for {
		select {
		case text := <-actions:
			log.Printf("Echo: %s", text)
			playTTS(call, cfg, text, "af_sky")
		case <-done:
			log.Printf("Call ended from %s", call.From())
			return
		}
	}
}

// =============================================================================
// Energy VAD
// =============================================================================

// rmsEnergy computes the Root Mean Square energy of a PCM frame.
// Higher values = louder audio. Used to distinguish speech from silence.
func rmsEnergy(samples []int16) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// =============================================================================
// Whisper STT (Speech-to-Text via HTTP)
// =============================================================================
//
// Uses the OpenAI-compatible /v1/audio/transcriptions endpoint provided by
// faster-whisper-server. We encode the PCM samples as a WAV file and POST
// it as multipart/form-data.

type sttResponse struct {
	Text string `json:"text"`
}

// transcribe sends a PCM audio buffer to Whisper and returns the transcript.
func transcribe(sttURL string, samples []int16) (string, error) {
	// Encode raw PCM samples as a WAV file (Whisper expects a file, not raw PCM).
	wavData := encodeWAV(samples, 8000)

	// Build multipart form: file=audio.wav, model=..., response_format=json
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wavData); err != nil {
		return "", err
	}
	if err := w.WriteField("model", "Systran/faster-whisper-tiny.en"); err != nil {
		return "", err
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	w.Close()

	resp, err := http.Post(sttURL+"/v1/audio/transcriptions", w.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("STT %d: %s", resp.StatusCode, string(b))
	}

	var r sttResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// =============================================================================
// Kokoro TTS (Text-to-Speech via HTTP)
// =============================================================================
//
// Uses the OpenAI-compatible /v1/audio/speech endpoint provided by kokoro-fastapi.
// We request raw PCM output at 24kHz, then downsample to 8kHz for telephony.

// playTTS synthesizes text and plays it to the caller.
func playTTS(call xphone.Call, cfg *localConfig, text, voice string) {
	pcmBytes, err := fetchTTS(cfg.ttsURL, text, voice)
	if err != nil {
		log.Printf("TTS failed: %v", err)
		return
	}

	// Kokoro outputs 24kHz PCM. Telephony uses 8kHz.
	// Downsample by taking every 3rd sample (24000/3 = 8000).
	samples24k := bytesToPCM(pcmBytes)
	samples8k := downsample3x(samples24k)

	// PacedPCMWriter sends audio at real-time pace (20ms per frame).
	// This prevents dumping all audio at once, which would overflow buffers.
	writer := call.PacedPCMWriter()
	for _, frame := range pcmToFrames(samples8k) {
		writer <- frame
	}
}

// fetchTTS calls the Kokoro API and returns raw PCM bytes at 24kHz.
func fetchTTS(ttsURL, text, voice string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{
		"model":           "kokoro",
		"input":           text,
		"voice":           voice,
		"response_format": "pcm",
	})
	req, err := http.NewRequest("POST", ttsURL+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TTS %d: %s", resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}

// =============================================================================
// Audio helpers
// =============================================================================

// encodeWAV creates a RIFF WAV file (PCM16 little-endian, mono) from samples.
// This is needed because Whisper expects a file upload, not raw PCM bytes.
// The WAV format is simple: 44-byte header + raw sample data.
func encodeWAV(samples []int16, sampleRate int) []byte {
	dataSize := len(samples) * 2
	fileSize := 36 + dataSize

	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(fileSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)                   // fmt chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)                    // PCM format
	binary.LittleEndian.PutUint16(buf[22:24], 1)                    // mono
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))   // sample rate
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2)) // byte rate (sampleRate * channels * bitsPerSample/8)
	binary.LittleEndian.PutUint16(buf[32:34], 2)                    // block align (channels * bitsPerSample/8)
	binary.LittleEndian.PutUint16(buf[34:36], 16)                   // bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))

	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(s))
	}
	return buf
}

// downsample3x reduces 24kHz samples to 8kHz by taking every 3rd sample.
// This is a simple decimation — no anti-aliasing filter, but good enough
// for speech over telephone-quality audio.
func downsample3x(samples []int16) []int16 {
	out := make([]int16, len(samples)/3)
	for i := range out {
		out[i] = samples[i*3]
	}
	return out
}

// bytesToPCM converts little-endian PCM16 bytes (as returned by Kokoro) to samples.
func bytesToPCM(data []byte) []int16 {
	n := len(data) / 2
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return samples
}

// pcmToFrames splits a sample buffer into 160-sample frames (20ms @ 8kHz).
// Each frame is one RTP packet's worth of audio — the unit that
// PacedPCMWriter expects.
func pcmToFrames(samples []int16) [][]int16 {
	const frameSize = 160 // 8000 Hz * 0.020s = 160 samples per 20ms frame
	var frames [][]int16
	for i := 0; i < len(samples); i += frameSize {
		end := i + frameSize
		if end > len(samples) {
			end = len(samples)
		}
		frame := make([]int16, end-i)
		copy(frame, samples[i:end])
		frames = append(frames, frame)
	}
	return frames
}

// =============================================================================
// Env helpers
// =============================================================================

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("Required env var %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
