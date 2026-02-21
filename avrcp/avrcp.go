// Package avrcp reads playback state from the BlueZ org.bluez.MediaPlayer1
// D-Bus interface (AVRCP) by shelling out to busctl. This avoids external
// Go D-Bus library dependencies.
package avrcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// PlayState is the complete snapshot of AVRCP playback state.
type PlayState struct {
	Title    string
	Artist   string
	Album    string
	Duration uint32 // milliseconds; 0 if unknown
	Position uint32 // milliseconds
	Playing  bool
}

// Source polls BlueZ via busctl for AVRCP MediaPlayer1 state.
type Source struct {
	mu          sync.RWMutex
	state       PlayState
	lastTrackLog string // dedup track log lines
}

func newPlayState() PlayState {
	// Default to playing=true and a non-zero duration so the car doesn't
	// see a paused/empty state before the first successful AVRCP poll.
	return PlayState{Playing: true, Duration: 300_000}
}

// NewSource starts a background AVRCP poller and returns immediately.
// It never returns an error — polling failures are handled gracefully.
func NewSource() (*Source, error) {
	s := &Source{state: newPlayState()}
	go s.loop()
	return s, nil
}

// PlaybackStatus implements extremote.DeviceExtRemote.
// Returns (trackLength ms, trackPosition ms, playing).
func (s *Source) PlaybackStatus() (trackLength, trackPos uint32, playing bool) {
	st := s.snapshot()
	dur := st.Duration
	if dur == 0 {
		dur = 300_000 // 5-minute fallback when duration unknown
	}
	return dur, st.Position, st.Playing
}

// TrackTitle implements extremote.DeviceExtRemote.
func (s *Source) TrackTitle() string {
	t := s.snapshot().Title
	if t == "" {
		return "Bluetooth"
	}
	return t
}

// TrackArtist implements extremote.DeviceExtRemote.
func (s *Source) TrackArtist() string { return s.snapshot().Artist }

// TrackAlbum implements extremote.DeviceExtRemote.
func (s *Source) TrackAlbum() string { return s.snapshot().Album }

// TrackPositionMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackPositionMs() uint32 { return s.snapshot().Position }

// TrackLengthMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackLengthMs() uint32 {
	st := s.snapshot()
	dur := st.Duration
	if dur == 0 {
		dur = 300_000
	}
	return dur
}

func (s *Source) snapshot() PlayState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Source) loop() {
	for {
		s.refresh()
		time.Sleep(500 * time.Millisecond)
	}
}

// findPlayerPath returns the D-Bus object path of the first BlueZ
// MediaPlayer1 object by parsing `busctl tree org.bluez`.
func findPlayerPath() string {
	out, err := exec.Command("busctl", "--system", "tree", "org.bluez").Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Match leaf player objects like /org/bluez/hci0/dev_.../player0
		if strings.Contains(line, "/player") && !strings.Contains(line, "/player/") &&
			!strings.Contains(line, "NowPlaying") && !strings.Contains(line, "Filesystem") {
			// strip tree-drawing characters
			path := strings.TrimLeft(line, "├─└│ ")
			if strings.HasPrefix(path, "/org/bluez") {
				return path
			}
		}
	}
	return ""
}

// busctlVariant holds the minimal structure returned by busctl --json=short.
type busctlVariant struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// getProperty calls `busctl --system --json=short get-property ...` and returns
// the parsed variant. Returns nil on any error.
func getProperty(path, iface, prop string) *busctlVariant {
	out, err := exec.Command("busctl", "--system", "--json=short",
		"get-property", "org.bluez", path, iface, prop).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var v busctlVariant
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return nil
	}
	return &v
}

func (s *Source) logOnce(msg string) {
	s.mu.Lock()
	if s.lastTrackLog != msg {
		s.lastTrackLog = msg
		s.mu.Unlock()
		log.Print(msg)
	} else {
		s.mu.Unlock()
	}
}

func (s *Source) refresh() {
	path := findPlayerPath()
	if path == "" {
		return
	}

	// Start from the existing state so we never lose previously-cached
	// metadata (Title/Artist/Album) on a poll cycle where Track is absent.
	s.mu.RLock()
	next := s.state
	s.mu.RUnlock()

	gotAny := false

	// Position (uint32, milliseconds)
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Position"); v != nil {
		var pos uint32
		if json.Unmarshal(v.Data, &pos) == nil {
			next.Position = pos
			gotAny = true
		}
	}

	// Status ("playing" | "paused" | "stopped" | "forward-seek" | "reverse-seek" | "error")
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Status"); v != nil {
		var status string
		if json.Unmarshal(v.Data, &status) == nil {
			next.Playing = status == "playing"
			gotAny = true
		}
	}

	// Track is an a{sv} dict — busctl renders it as a JSON object.
	// We parse Title, Artist, Album, Duration if present.
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Track"); v != nil {
		var track map[string]busctlVariant
		if json.Unmarshal(v.Data, &track) == nil {
			if t, ok := track["Title"]; ok {
				var title string
				if json.Unmarshal(t.Data, &title) == nil && title != "" {
					next.Title = title
					gotAny = true
				}
			}
			if a, ok := track["Artist"]; ok {
				var artist string
				if json.Unmarshal(a.Data, &artist) == nil {
					next.Artist = artist
					gotAny = true
				}
			}
			if a, ok := track["Album"]; ok {
				var album string
				if json.Unmarshal(a.Data, &album) == nil {
					next.Album = album
					gotAny = true
				}
			}
			if d, ok := track["Duration"]; ok {
				var dur uint32
				if json.Unmarshal(d.Data, &dur) == nil && dur > 0 {
					next.Duration = dur
					gotAny = true
				}
			}
			msg := fmt.Sprintf("[AVRCP] track: title=%q artist=%q album=%q duration=%d",
				next.Title, next.Artist, next.Album, next.Duration)
			s.logOnce(msg)
		} else {
			s.logOnce("[AVRCP] track: failed to parse Track JSON")
		}
	} else {
		s.logOnce("[AVRCP] track property not available from " + path)
	}

	if !gotAny {
		return
	}
	s.mu.Lock()
	s.state = next
	s.mu.Unlock()
}
