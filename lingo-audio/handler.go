package audio

import (
	"github.com/oandrew/ipod"
)

type DeviceAudio interface {
	// SetSampleRate is called once the best mutually-supported sample rate has
	// been selected so the audio stack can reconfigure itself accordingly.
	SetSampleRate(rate uint32)
}

// func ackSuccess(req ipod.Packet) ACK {
// 	return ACK{Status: ACKStatusSuccess, CmdID: uint8(req.ID.CmdID())}
// }

// func ackPending(req ipod.Packet, maxWait uint32) ACKPending {
// 	return ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
// }

// negotiatedRate is the sample rate agreed with the car during the audio
// handshake.  It is re-sent with each TrackNewAudioAttributes so the car
// reopens its audio stream after every PlayCurrentSelection.
var negotiatedRate uint32 = 44100

func Start(tr ipod.CommandWriter) {
	ipod.Send(tr, &GetAccSampleRateCaps{})
}

// ReopenAudio re-sends TrackNewAudioAttributes using the last negotiated rate.
// Call this after PlayCurrentSelection so the car reopens its audio interface.
func ReopenAudio(tr ipod.CommandWriter) {
	ipod.Send(tr, &TrackNewAudioAttributes{
		SampleRate: negotiatedRate,
	})
}

func HandleAudio(req *ipod.Command, tr ipod.CommandWriter, dev DeviceAudio) error {
	switch msg := req.Payload.(type) {
	case *AccAck:
		// Car acknowledges our sample rate caps request
		// No additional action needed

	case *RetAccSampleRateCaps:
		// Prefer 48000 Hz to match BlueALSA's aptX output and avoid a
		// resample.  If the car doesn't advertise 48000, fall back to the
		// highest rate it does support, then 44100 as a last resort.
		const preferredRate uint32 = 48000
		var bestRate uint32 = 44100
		hasPreferred := false
		for _, rate := range msg.SampleRates {
			if rate == preferredRate {
				hasPreferred = true
			}
			if rate > bestRate {
				bestRate = rate
			}
		}
		if hasPreferred {
			bestRate = preferredRate
		}

		negotiatedRate = bestRate

		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x03, // RetAccSampleRateCaps command ID
		})
		// Inform the car which rate we will stream at.
		ipod.Send(tr, &TrackNewAudioAttributes{
			SampleRate: bestRate,
		})
		// Notify the local audio stack so it can open ALSA at this rate.
		if dev != nil {
			dev.SetSampleRate(bestRate)
		}

	case *TrackNewAudioAttributes:
		// Car sends audio attributes and is ready for audio
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x04, // TrackNewAudioAttributes command ID
		})

	case *SetVideoDelay:
		// Car sets video delay offset
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x05, // SetVideoDelay command ID
		})

	default:
		_ = msg
	}
	return nil
}
