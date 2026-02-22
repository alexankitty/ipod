package extremote

import (
	"time"

	audio "github.com/oandrew/ipod/lingo-audio"

	"github.com/oandrew/ipod"
)

type DeviceExtRemote interface {
	// PlaybackStatus returns track duration (ms), current position (ms), and
	// whether the player is currently playing (vs paused/stopped).
	PlaybackStatus() (trackLength, trackPos uint32, playing bool)
	TrackTitle() string
	TrackArtist() string
	TrackAlbum() string
	// MediaControl sends a playback command to the phone via AVRCP.
	// method is a BlueZ MediaPlayer1 method name: "Play", "Pause",
	// "Next", "Previous", "FastForward", "Rewind".
	MediaControl(method string)
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: req.ID.CmdID()}
}

// audioAttrDebounce is the minimum interval between consecutive
// TrackNewAudioAttributes sends. The car's stream-reopen cycle takes ~500ms
// and causes it to send another PlayCurrentSelection; without debouncing this
// creates a tight feedback loop.
const audioAttrDebounce = 5 * time.Second

// ExtRemoteHandler manages session-scoped state for lingo 0x04 (Extended Remote).
// A new instance must be created for each USB session so that playing state
// resets correctly on reconnect.
type ExtRemoteHandler struct {
	// playing is false until the first PlayControl is received.  Keeping it
	// false initially means GetPlayStatus returns Paused, which causes the car
	// to treat the first Toggle as "start playing" → single PlayControl cycle
	// → PlayCurrentSelection arrives well within the car's 3.716-second
	// audio-open window.
	playing bool
	// audioEstablished is true once TrackNewAudioAttributes has been sent at
	// least once this session. Starts true so the IDPS send (from the audio
	// lingo) counts — suppressing spurious TrackIndex pushes from notifyCh
	// during start-up before the first PlayCurrentSelection.
	audioEstablished bool
	// lastAudioAttrSent is the time we last sent TrackNewAudioAttributes.
	// Per spec, TrackNewAudioAttributes must be sent on every PlayCurrentSelection
	// to (re)open the USB audio stream. However the car's stream-reopen cycle
	// itself triggers another PlayCurrentSelection, so we debounce resends
	// within audioAttrDebounce to break the feedback loop.
	// Zero value means "never sent" — PlayCurrentSelection will always fire it.
	lastAudioAttrSent time.Time
}

// NewExtRemoteHandler returns a handler with playing=false (paused initial
// state) and audioEstablished=true. The audio lingo always sends
// TrackNewAudioAttributes during IDPS before any ExtRemote commands arrive,
// so the stream is already open. Starting audioEstablished=true prevents
// spurious TrackIndex pushes from notifyCh during start-up.
// lastAudioAttrSent starts zero so the first PlayCurrentSelection always
// sends TrackNewAudioAttributes (bypassing the debounce).
func NewExtRemoteHandler() *ExtRemoteHandler {
	return &ExtRemoteHandler{audioEstablished: true}
}

// IsPlaying reports the current playing state, used externally to build
// async PlayStatusChangeNotification messages.
func (h *ExtRemoteHandler) IsPlaying() bool { return h.playing }

// AudioEstablished reports whether the USB audio stream has been opened at
// least once this session, used to gate spontaneous TrackIndex pushes from
// the AVRCP notifyCh.
func (h *ExtRemoteHandler) AudioEstablished() bool { return h.audioEstablished }

func (h *ExtRemoteHandler) playerState() PlayerState {
	if h.playing {
		return PlayerStatePlaying
	}
	return PlayerStatePaused
}

// HandleExtRemote is kept for callers that don't need session state.
// Prefer ExtRemoteHandler.Handle for new code.
func HandleExtRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	h := ExtRemoteHandler{playing: true} // legacy: assume playing
	return h.Handle(req, tr, dev)
}

func (h *ExtRemoteHandler) Handle(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	//log.Printf("Req: %#v", req)
	switch msg := req.Payload.(type) {

	case *GetCurrentPlayingTrackChapterInfo:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterInfo{
			CurrentChapterIndex: 0,
			ChapterCount:        1,
		})
	case *SetCurrentPlayingTrackChapter:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetCurrentPlayingTrackChapterPlayStatus:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterPlayStatus{
			ChapterPosition: 0,
			ChapterLength:   0,
		})
	case *GetCurrentPlayingTrackChapterName:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterName{
			ChapterName: ipod.StringToBytes("chapter"),
		})
	case *GetAudiobookSpeed:
		ipod.Respond(req, tr, &ReturnAudiobookSpeed{
			Speed: 0,
		})
	case *SetAudiobookSpeed:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetIndexedPlayingTrackInfo:
		var info interface{}
		switch msg.InfoType {
		case TrackInfoCaps:
			capLength := uint32(300_000)
			if dev != nil {
				cl, _, _ := dev.PlaybackStatus()
				if cl > 0 {
					capLength = cl
				}
			}
			info = &TrackCaps{
				Caps:         0x0,
				TrackLength:  capLength,
				ChapterCount: 1,
			}
		case TrackInfoDescription, TrackInfoLyrics:
			info = &TrackLongText{
				Flags:       0x0,
				PacketIndex: 0,
				Text:        0x00,
			}
		case TrackInfoArtworkCount:
			info = struct{}{}
		default:
			info = []byte{0x00}

		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
			Info:     info,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{})
	case *GetTrackArtworkData:
		ipod.Respond(req, tr, &ACK{
			Status: ACKStatusFailed,
			CmdID:  req.ID.CmdID(),
		})
	case *ResetDBSelection:
		// The car sends ResetDBSelection during DB browsing (e.g. after a
		// TrackIndex notification). We just ack it. Do NOT reset
		// audioEstablished here — doing so caused a spurious TrackIndex push
		// from the next PlayControl, which triggered another 8-deep
		// ResetDBSelection storm with no PlayCurrentSelection ever following.
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetNumberCategorizedDBRecords:
		// Per rockbox iap-lingo4.c and libiap: returning 0 for Playlist and
		// Track causes some head units (e.g. Alpine) to hang or loop forever.
		// Return a non-zero dummy count for Playlist and Track; 0 for
		// categories we don't support (Genre, Composer, AudioBook, Podcast).
		var count int32
		switch msg.CategoryType {
		case DbCategoryPlaylist:
			count = 1 // at least one playlist (the current queue)
		case DbCategoryTrack, DbCategoryArtist, DbCategoryAlbum:
			count = 1 // at least one track playing
		default:
			count = 0
		}
		ipod.Respond(req, tr, &ReturnNumberCategorizedDBRecords{
			RecordCount: count,
		})
	case *RetrieveCategorizedDatabaseRecords:
		// The car fetches the record(s) it found via GetNumberCategorizedDBRecords.
		// We only have one virtual entry per category; return it regardless of
		// the requested Offset/Count.
		var name [16]byte
		copy(name[:], "Bluetooth")
		ipod.Respond(req, tr, &ReturnCategorizedDatabaseRecord{
			RecordCategoryIndex: 0,
			String:              name,
		})
	case *GetPlayStatus:
		length, pos := uint32(300_000), uint32(0)
		if dev != nil {
			length, pos, _ = dev.PlaybackStatus()
		}
		// Only extend length when we have no real duration (live streams / not
		// yet received). If we have a real AVRCP duration, trust it — the
		// +300s guard was causing "-5:00" to show at the start of every track.
		if length == 0 {
			length = pos + 300_000
		}
		ipod.Respond(req, tr, &ReturnPlayStatus{
			TrackLength:   length,
			TrackPosition: pos,
			State:         h.playerState(),
		})
	case *GetCurrentPlayingTrackIndex:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackIndex{
			TrackIndex: 0,
		})
	case *GetIndexedPlayingTrackTitle:
		title := "Bluetooth"
		if dev != nil {
			title = dev.TrackTitle()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackTitle{
			Title: ipod.StringToBytes(ipod.TruncateRunes(title, 20)),
		})
	case *GetIndexedPlayingTrackArtistName:
		artist := ""
		if dev != nil {
			artist = dev.TrackArtist()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackArtistName{
			ArtistName: ipod.StringToBytes(ipod.TruncateRunes(artist, 20)),
		})
	case *GetIndexedPlayingTrackAlbumName:
		album := ""
		if dev != nil {
			album = dev.TrackAlbum()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackAlbumName{
			AlbumName: ipod.StringToBytes(ipod.TruncateRunes(album, 20)),
		})
	case *SetPlayStatusChangeNotification:
		ipod.Respond(req, tr, ackSuccess(req))
		// Push Paused + TrackIndex(0). The car uses EventID=0x00 as a trigger
		// to start its PlayControl(Toggle) sequence. This specific format
		// (0x00 byte + PlayerState byte) is what this car expects, even though
		// the spec defines 0x00 as PlaybackStopped with no extra data.
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00,
			PlayerState: byte(PlayerStatePaused),
		})
	case *SetPlayStatusChangeNotificationShort:
		ipod.Respond(req, tr, ackSuccess(req))
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00,
			PlayerState: byte(PlayerStatePaused),
		})
	case *PlayCurrentSelection:
		h.playing = true
		if dev != nil {
			dev.MediaControl("Play")
		}
		ipod.Respond(req, tr, ackSuccess(req))
		// Per iAP spec, the accessory must send TrackNewAudioAttributes on every
		// PlayCurrentSelection to open/reopen the USB audio stream. However,
		// the car's stream-reopen cycle itself causes another PlayCurrentSelection
		// to arrive ~500ms later, creating a feedback loop if we resend
		// immediately. Debounce: skip the resend if we sent one within the last
		// audioAttrDebounce window.
		if time.Since(h.lastAudioAttrSent) >= audioAttrDebounce {
			h.lastAudioAttrSent = time.Now()
			h.audioEstablished = true
			ipod.Send(tr, &audio.TrackNewAudioAttributes{SampleRate: audio.NegotiatedRate()})
		}
	case *PlayControl:
		wasPlaying := h.playing
		// Determine the BlueZ MediaPlayer1 method to call on the phone.
		var avrcpCmd string
		switch msg.Cmd {
		case PlayControlToggle:
			if wasPlaying {
				// Already playing — keep it playing and don't change state.
				// The car sends PlayControl(Toggle) every ~30s as a renegotiation
				// ping while audio is active. If we respond Paused, the car closes
				// USB audio and sends PlayCurrentSelection, causing a dropout.
				// Responding Playing (i.e. no state change) makes the car accept
				// the current state and leave the audio stream alone.
				h.playing = true
				avrcpCmd = "" // don't touch the phone
			} else {
				// Was paused — start playing.
				h.playing = true
				avrcpCmd = "Play"
				// Reopen the audio stream at the negotiated rate so the car uses
				// the correct sample rate when resuming from pause.
				ipod.Send(tr, &audio.TrackNewAudioAttributes{SampleRate: audio.NegotiatedRate()})
			}
		case PlayControlPlay:
			h.playing = true
			avrcpCmd = "Play"
			// Reopen the audio stream at the negotiated rate.
			ipod.Send(tr, &audio.TrackNewAudioAttributes{SampleRate: audio.NegotiatedRate()})
		case PlayControlPause:
			h.playing = false
			avrcpCmd = "Pause"
			// Notify the car of the paused state at the current negotiated rate.
			ipod.Send(tr, &audio.TrackNewAudioAttributes{SampleRate: audio.NegotiatedRate()})
		case PlayControlStop:
			h.playing = false
			avrcpCmd = "Pause"
		case PlayControlNextTrack, PlayControlNext, PlayControlNextChapter:
			avrcpCmd = "Next"
			// Car will close the USB audio stream when the track changes;
			// reset debounce so the following PlayCurrentSelection reopens it.
			h.lastAudioAttrSent = time.Time{}
		case PlayControlPrevTrack, PlayControlPrev, PlayControlPrevChapter:
			avrcpCmd = "Previous"
			// Same as Next — car closes stream on track change.
			h.lastAudioAttrSent = time.Time{}
		case PlayControlStartFF:
			avrcpCmd = "FastForward"
		case PlayControlStartRew:
			avrcpCmd = "Rewind"
		case PlayControlEndFFRew:
			avrcpCmd = "Release"
		}
		if avrcpCmd != "" && dev != nil {
			dev.MediaControl(avrcpCmd)
		}
		ipod.Respond(req, tr, ackSuccess(req))
		// Confirm the state change. This specific 0x00+PlayerState format is
		// what the car expects from PlayControl.
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00,
			PlayerState: byte(h.playerState()),
		})
		// When we notify the car we are Paused, do NOT reset audioEstablished.
		// The ~30s renegotiation cycle is: Toggle→Paused → immediate PlayCurrentSelection.
		// If we reset here, PlayCurrentSelection will send TrackNewAudioAttributes,
		// causing the car to tear down and reopen USB audio — creating the very
		// dropout we're trying to avoid. Audio is only truly closed by the car
		// after a track skip (handled above in Next/Prev) or USB disconnect.
		// Do NOT push TrackIndexChanged here. This car never responds to a
		// TrackIndex push from PlayControl with PlayCurrentSelection — it just
		// launches an 8-deep ResetDBSelection browse storm and then goes quiet.
		// Track-change notifications are handled by the AVRCP notifyCh path
		// in main.go instead.
	case *GetTrackArtworkTimes:
		ipod.Respond(req, tr, &RetTrackArtworkTimes{})
	case *GetShuffle:
		ipod.Respond(req, tr, &ReturnShuffle{Mode: ShuffleOff})
	case *SetShuffle:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetRepeat:
		ipod.Respond(req, tr, &ReturnRepeat{Mode: RepeatOff})
	case *SetRepeat:
		ipod.Respond(req, tr, ackSuccess(req))

	case *SetDisplayImage:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetMonoDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnMonoDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *GetNumPlayingTracks:
		ipod.Respond(req, tr, &ReturnNumPlayingTracks{
			NumTracks: 1,
		})
	case *SetCurrentPlayingTrack:
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectSortDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetColorDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnColorDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *ResetDBSelectionHierarchy:
		ipod.Respond(req, tr, &ACK{Status: ACKStatusFailed, CmdID: req.ID.CmdID()})

	case *GetDBiTunesInfo:
	// RetDBiTunesInfo:
	case *GetUIDTrackInfo:
	// RetUIDTrackInfo:
	case *GetDBTrackInfo:
	// RetDBTrackInfo:
	case *GetPBTrackInfo:
	// RetPBTrackInfo:

	default:
		_ = msg
	}
	return nil
}
