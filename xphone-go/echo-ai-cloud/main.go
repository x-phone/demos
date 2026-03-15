package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/x-phone/xphone-go"
)

func main() {
	sipUser := requireEnv("SIP_USERNAME")
	sipPass := requireEnv("SIP_PASSWORD")
	sipHost := requireEnv("SIP_HOST")
	sipTransport := envOr("SIP_TRANSPORT", "udp")
	dgKey := requireEnv("DEEPGRAM_API_KEY")

	phone := xphone.New(
		xphone.WithCredentials(sipUser, sipPass, sipHost),
		xphone.WithTransport(sipTransport, nil),
		xphone.WithCodecs(xphone.CodecPCMU, xphone.CodecPCMA),
	)

	phone.OnIncoming(func(call xphone.Call) {
		log.Printf("Incoming call from %s", call.From())
		if err := call.Accept(); err != nil {
			log.Printf("Accept failed: %v", err)
			return
		}
		go handleCall(call, dgKey)
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("Connecting to %s as %s (%s)...", sipHost, sipUser, sipTransport)
	if err := phone.Connect(ctx); err != nil {
		log.Fatalf("Connect failed: %v", err)
	}
	log.Println("Registered — waiting for calls")

	<-ctx.Done()
	log.Println("Shutting down...")
	phone.Disconnect()
}

// handleCall runs the echo-AI loop for a single call.
func handleCall(call xphone.Call, dgKey string) {
	actions := make(chan string, 8)
	done := make(chan struct{})

	call.OnEnded(func(_ xphone.EndReason) {
		close(done)
	})

	call.OnDTMF(func(digit string) {
		select {
		case actions <- fmt.Sprintf("You pressed: %s", digit):
		default:
		}
	})

	// Start Deepgram STT WebSocket.
	sttConn, err := dialSTT(dgKey)
	if err != nil {
		log.Printf("STT connect failed: %v", err)
		return
	}
	defer sttConn.Close()

	// Goroutine A: caller PCM → STT
	go func() {
		pcmCh := call.PCMReader()
		for {
			select {
			case samples, ok := <-pcmCh:
				if !ok {
					return
				}
				if err := sttConn.WriteMessage(websocket.BinaryMessage, pcmToBytes(samples)); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Goroutine B: STT results → actions channel
	go func() {
		for {
			_, msg, err := sttConn.ReadMessage()
			if err != nil {
				return
			}
			text := parseSTTResult(msg)
			if text != "" {
				select {
				case actions <- fmt.Sprintf("You said: %s", text):
				default:
				}
			}
		}
	}()

	// Play greeting.
	playTTS(call, dgKey, "Hello! Say something and I'll repeat it back to you. You can also press any key.", "aura-asteria-en")

	// Main loop: wait for actions or call end.
	for {
		select {
		case text := <-actions:
			log.Printf("Echo: %s", text)
			playTTS(call, dgKey, text, "aura-2-apollo-en")
		case <-done:
			log.Printf("Call ended from %s", call.From())
			return
		}
	}
}

// --- Deepgram STT (WebSocket) ------------------------------------------------

func dialSTT(apiKey string) (*websocket.Conn, error) {
	url := "wss://api.deepgram.com/v1/listen?encoding=linear16&sample_rate=8000&channels=1&model=nova-2&punctuate=true&interim_results=false&endpointing=300"
	header := http.Header{"Authorization": []string{"Token " + apiKey}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	return conn, err
}

type sttResponse struct {
	Type    string `json:"type"`
	Channel struct {
		Alternatives []struct {
			Transcript string `json:"transcript"`
		} `json:"alternatives"`
	} `json:"channel"`
	SpeechFinal bool `json:"speech_final"`
}

func parseSTTResult(msg []byte) string {
	var r sttResponse
	if err := json.Unmarshal(msg, &r); err != nil {
		return ""
	}
	if r.Type != "Results" || !r.SpeechFinal {
		return ""
	}
	if len(r.Channel.Alternatives) == 0 {
		return ""
	}
	return r.Channel.Alternatives[0].Transcript
}

// --- Deepgram TTS (HTTP) -----------------------------------------------------

func playTTS(call xphone.Call, apiKey, text, voice string) {
	pcmBytes, err := fetchTTS(apiKey, text, voice)
	if err != nil {
		log.Printf("TTS failed: %v", err)
		return
	}
	writer := call.PacedPCMWriter()
	for _, frame := range bytesToPCMFrames(pcmBytes) {
		writer <- frame
	}
}

func fetchTTS(apiKey, text, voice string) ([]byte, error) {
	url := fmt.Sprintf("https://api.deepgram.com/v1/speak?model=%s&encoding=linear16&sample_rate=8000&container=none", voice)
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+apiKey)
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

// --- Audio helpers -----------------------------------------------------------

// pcmToBytes converts PCM16 samples to little-endian bytes (for STT).
func pcmToBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

// bytesToPCMFrames converts little-endian PCM16 bytes to 160-sample frames (20ms @ 8kHz).
func bytesToPCMFrames(data []byte) [][]int16 {
	const frameSize = 160
	totalSamples := len(data) / 2
	var frames [][]int16
	for i := 0; i < totalSamples; i += frameSize {
		end := i + frameSize
		if end > totalSamples {
			end = totalSamples
		}
		frame := make([]int16, end-i)
		for j := range frame {
			frame[j] = int16(binary.LittleEndian.Uint16(data[(i+j)*2:]))
		}
		frames = append(frames, frame)
	}
	return frames
}

// --- Env helpers -------------------------------------------------------------

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
