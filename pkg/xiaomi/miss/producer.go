package miss

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

type Producer struct {
	core.Connection
	client *Client
	fps    uint32
}

func Dial(rawURL string) (core.Producer, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	fps, err := parseFPS(u.Query().Get("fps"))
	if err != nil {
		return nil, err
	}

	client, err := NewClient(rawURL)
	if err != nil {
		return nil, err
	}

	query := u.Query()

	err = client.StartMedia(query.Get("channel"), query.Get("subtype"), query.Get("audio"))
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	medias, err := probe(client, query.Get("audio") != "0")
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Producer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "xiaomi/miss",
			Protocol:   client.Protocol(),
			RemoteAddr: client.RemoteAddr().String(),
			UserAgent:  client.Version(),
			Medias:     medias,
			Transport:  client,
		},
		client: client,
		fps:    fps,
	}, nil
}

func parseFPS(s string) (uint32, error) {
	if s == "" {
		return 0, nil
	}

	fps, err := strconv.ParseUint(s, 10, 32)
	if err != nil || fps == 0 || fps > 120 {
		return 0, fmt.Errorf("xiaomi: invalid fps %q", s)
	}

	return uint32(fps), nil
}

func probe(client *Client, audio bool) ([]*core.Media, error) {
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	var vcodec, acodec *core.Codec

	for {
		pkt, err := client.ReadPacket()
		if err != nil {
			if vcodec != nil {
				err = fmt.Errorf("no audio")
			} else if acodec != nil {
				err = fmt.Errorf("no video")
			}
			return nil, fmt.Errorf("xiaomi: probe: %w", err)
		}

		switch pkt.CodecID {
		case codecH264:
			if vcodec == nil {
				buf := annexb.EncodeToAVCC(pkt.Payload)
				if h264.NALUType(buf) == h264.NALUTypeSPS {
					vcodec = h264.AVCCToCodec(buf)
				}
			}
		case codecH265:
			if vcodec == nil {
				buf := annexb.EncodeToAVCC(pkt.Payload)
				if h265.NALUType(buf) == h265.NALUTypeVPS {
					vcodec = h265.AVCCToCodec(buf)
				}
			}
		case codecPCMA:
			if acodec == nil {
				acodec = &core.Codec{Name: core.CodecPCMA, ClockRate: pkt.SampleRate()}
			}
		case codecOPUS:
			if acodec == nil {
				acodec = &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2}
			}
		}

		if vcodec != nil && (acodec != nil || !audio) {
			break
		}
	}

	_ = client.SetDeadline(time.Time{})

	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{vcodec},
		},
	}

	if acodec != nil {
		medias = append(medias, &core.Media{
			Kind:      core.KindAudio,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{acodec},
		})

		medias = append(medias, &core.Media{
			Kind:      core.KindAudio,
			Direction: core.DirectionSendonly,
			Codecs:    []*core.Codec{acodec.Clone()},
		})
	}

	return medias, nil
}

const (
	videoClockRate     = 90000
	opusClockRate      = 48000
	opusDefaultSamples = opusClockRate * 40 / 1000
)

type videoTimestampNormalizer struct {
	fps       uint64
	started   bool
	source    uint64
	timestamp uint32
}

func (n *videoTimestampNormalizer) normalize(source uint64) uint32 {
	if n.fps == 0 {
		return TimeToRTP(source, videoClockRate)
	}

	if !n.started {
		n.started = true
		n.source = source
		n.timestamp = TimeToRTP(source, videoClockRate)
		return n.timestamp
	}

	// A frame may be split into multiple NAL units. Keep all packets with the
	// same camera timestamp in the same RTP access unit.
	if source == n.source {
		return n.timestamp
	}

	frames := uint64(1)
	if source > n.source {
		// Quantize camera millisecond jitter to the configured frame interval,
		// while preserving real gaps when one or more frames were dropped.
		frames = ((source-n.source)*n.fps + 500) / 1000
		if frames == 0 {
			frames = 1
		}
	}

	n.source = source
	n.timestamp += uint32(frames * videoClockRate / n.fps)
	return n.timestamp
}

func opusPacketSamples(payload []byte) uint32 {
	if len(payload) == 0 {
		return opusDefaultSamples
	}

	config := payload[0] >> 3
	var frameSamples uint32
	switch {
	case config < 12: // SILK: 10, 20, 40 or 60 ms
		frameSamples = [...]uint32{480, 960, 1920, 2880}[config&3]
	case config < 16: // Hybrid: 10 or 20 ms
		frameSamples = [...]uint32{480, 960}[config&1]
	default: // CELT: 2.5, 5, 10 or 20 ms
		frameSamples = [...]uint32{120, 240, 480, 960}[config&3]
	}

	var frames uint32
	switch payload[0] & 3 {
	case 0:
		frames = 1
	case 1, 2:
		frames = 2
	case 3:
		if len(payload) < 2 {
			return opusDefaultSamples
		}
		frames = uint32(payload[1] & 0x3F)
	}

	samples := frameSamples * frames
	if frames == 0 || samples > opusClockRate*120/1000 {
		return opusDefaultSamples
	}
	return samples
}

func (p *Producer) Start() error {
	var videoSeq, audioSeq uint16
	var audioTS uint32
	videoTS := videoTimestampNormalizer{fps: uint64(p.fps)}

	for {
		_ = p.client.SetDeadline(time.Now().Add(10 * time.Second))
		pkt, err := p.client.ReadPacket()
		if err != nil {
			return err
		}

		p.Recv += len(pkt.Payload)

		// TODO: rewrite this
		var name string
		var pkt2 *core.Packet

		switch pkt.CodecID {
		case codecH264, codecH265:
			pkt2 = &core.Packet{
				Header: rtp.Header{
					SequenceNumber: videoSeq,
					Timestamp:      videoTS.normalize(pkt.Timestamp),
				},
				Payload: annexb.EncodeToAVCC(pkt.Payload),
			}
			videoSeq++
			if pkt.CodecID == codecH264 {
				name = core.CodecH264
			} else {
				name = core.CodecH265
			}
		case codecPCMA:
			name = core.CodecPCMA
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: audioSeq,
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			audioSeq++
			audioTS += uint32(len(pkt.Payload))
		case codecOPUS:
			name = core.CodecOpus
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: audioSeq,
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			audioSeq++
			audioTS += opusPacketSamples(pkt.Payload)
		}

		for _, recv := range p.Receivers {
			if recv.Codec.Name == name {
				recv.WriteRTP(pkt2)
				break
			}
		}
	}
}

func (p *Producer) Stop() error {
	_ = p.client.StopMedia()
	return p.Connection.Stop()
}

func (p *Producer) SetDirection(operation int) error {
	return p.client.SetDirection(operation)
}

// TimeToRTP convert time in milliseconds to RTP time
func TimeToRTP(timeMS, clockRate uint64) uint32 {
	return uint32(timeMS * clockRate / 1000)
}
