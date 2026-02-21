// Package avrcp reads playback state from the BlueZ org.bluez.MediaPlayer1
// D-Bus interface (AVRCP) by shelling out to busctl. This avoids external
// Go D-Bus library dependencies.
package avrcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
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

// NewSource starts a background AVRCP poller and signal watcher, returning immediately.
// It never returns an error — polling failures are handled gracefully.
func NewSource() (*Source, error) {
	s := &Source{state: newPlayState()}
	go s.loop()
	go s.watchSignals()
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

// MediaControl calls a BlueZ MediaPlayer1 method on the phone to control
// playback via AVRCP. method is one of: Play, Pause, Stop, Next, Previous,
// FastForward, Rewind, Release.
func (s *Source) MediaControl(method string) {
	path := findPlayerPath()
	if path == "" {
		return
	}
	exec.Command("busctl", "--system", "call",
		"org.bluez", path,
		"org.bluez.MediaPlayer1", method).Run()
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

// watchSignals runs dbus-monitor and triggers an immediate refresh whenever
// the BlueZ MediaPlayer1 properties change (e.g. track skip, play/pause).
// This ensures Track metadata is captured as soon as the phone sends it,
// rather than waiting up to 500ms for the next poll.
func (s *Source) watchSignals() {
	for {
		cmd := exec.Command("dbus-monitor", "--system",
			"type=signal,sender=org.bluez,interface=org.freedesktop.DBus.Properties,member=PropertiesChanged")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		if err := cmd.Start(); err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			// Only react to player-path signals, not adapter-level ones.
			if strings.Contains(line, "/player") {
				go s.refresh()
			}
		}
		cmd.Wait()
		time.Sleep(time.Second)
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

// getAllProperties calls GetAll on org.bluez.MediaPlayer1 via busctl and returns
// the property map. Returns nil on any error.
func getAllProperties(path string) map[string]busctlVariant {
	out, err := exec.Command("busctl", "--system", "--json=short",
		"call", "org.bluez", path,
		"org.freedesktop.DBus.Properties", "GetAll",
		"s", "org.bluez.MediaPlayer1").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	// busctl wraps the reply: {"type":"a{sv}","data":[{...}]}
	var wrapper struct {
		Data []map[string]busctlVariant `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil || len(wrapper.Data) == 0 {
		return nil
	}
	return wrapper.Data[0]
}

// getString extracts a string value from a busctlVariant map.
func getString(props map[string]busctlVariant, key string) (string, bool) {
	v, ok := props[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v.Data, &s) != nil {
		return "", false
	}
	return s, true
}

// getUint32 extracts a uint32 value from a busctlVariant map.
func getUint32(props map[string]busctlVariant, key string) (uint32, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	var n uint32
	if json.Unmarshal(v.Data, &n) != nil {
		return 0, false
	}
	return n, true
}

func (s *Source) logOnce(msg string) {
	s.mu.Lock()
	if s.lastTrackLog != msg {
		s.lastTrackLog = msg
		s.mu.Unlock()
		fmt.Fprintln(os.Stderr, msg)
	} else {
		s.mu.Unlock()
	}
}

func (s *Source) refresh() {
	path := findPlayerPath()
	if path == "" {
		return
	}

	props := getAllProperties(path)
	if props == nil {
		return
	}

	// Start from the existing state so we never lose previously-cached
	// metadata (Title/Artist/Album) on a poll cycle where Track is absent.
	s.mu.RLock()
	next := s.state
	s.mu.RUnlock()

	gotAny := false

	if pos, ok := getUint32(props, "Position"); ok {
		next.Position = pos
		gotAny = true
	}

	if status, ok := getString(props, "Status"); ok {
		next.Playing = status == "playing"
		gotAny = true
	}

	// Track is an a{sv} nested inside the property map.
	// busctl renders it as {"type":"a{sv}","data":{"Title":{...},...}}.
	if trackProp, ok := props["Track"]; ok {
		var track map[string]busctlVariant
		if json.Unmarshal(trackProp.Data, &track) == nil && len(track) > 0 {
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
			s.logOnce("[AVRCP] Track property present but empty (phone not sending metadata)")
		}
	}

	if !gotAny {
		return
	}
	s.mu.Lock()
	s.state = next
	s.mu.Unlock()
}
