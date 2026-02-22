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

func Start(tr ipod.CommandWriter) {
	ipod.Send(tr, &GetAccSampleRateCaps{})
}

func HandleAudio(req *ipod.Command, tr ipod.CommandWriter, dev DeviceAudio) error {
	switch msg := req.Payload.(type) {
	case *AccAck:
		// Car acknowledges our sample rate caps request
		// No additional action needed

	case *RetAccSampleRateCaps:
		// Car sends the sample rates it supports.  Pick the highest one so we
		// get the best audio quality, falling back to 44100 if the list is
		// empty or contains no recognised value.
		var bestRate uint32 = 44100
		for _, rate := range msg.SampleRates {
			if rate > bestRate {
				bestRate = rate
			}
		}

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
