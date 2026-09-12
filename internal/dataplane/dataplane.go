package dataplane

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/protocols"
)

// tunOffset is the headroom in every buffer. CreateTUN enables GSO, so a
// Linux read prefixes each packet with a 10 byte virtio-net header, and a
// write reads it from the bytes before the offset. The offset must be at
// least 10.
const tunOffset = 16

// DataPlane runs the netstack over a TUN device and forwards every captured
// TCP, UDP, and ICMP echo flow of the protocol set to the remote helper over
// the mux.
type DataPlane struct {
	dev tun.Device
	ns  *netStack

	closed atomic.Bool
	wg     sync.WaitGroup
}

// New builds the netstack for the device MTU and wires it to the mux dialer.
// The device is the capture; the dialer is the remote side; the set says
// which protocols the client forwards.
func New(dev tun.Device, dialer mux.Dialer, mtu int, set protocols.Set) (*DataPlane, error) {
	ns, err := newNetStack(uint32(mtu), dialer, set)
	if err != nil {
		return nil, err
	}
	return &DataPlane{dev: dev, ns: ns}, nil
}

// Run starts the two packet pumps. It returns at once; Close and Wait end
// them.
func (d *DataPlane) Run(ctx context.Context) {
	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		d.tunToStack()
	}()
	go func() {
		defer d.wg.Done()
		d.stackToTun(ctx)
	}()
}

// Close ends the pumps and frees the netstack. It closes the device, which
// unblocks the read pump, and closes the link endpoint, which unblocks the
// write pump.
func (d *DataPlane) Close() error {
	d.closed.Store(true)
	d.ns.close()
	return d.dev.Close()
}

// Wait blocks until both pumps have returned.
func (d *DataPlane) Wait() {
	d.wg.Wait()
}

// tunToStack reads packets from the device in batches and injects each into
// the netstack. An ICMP echo request goes to its own flow instead, because
// the netstack has no forwarder for it.
func (d *DataPlane) tunToStack() {
	batch := d.dev.BatchSize()
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, tunOffset+header.UDPMaximumPacketSize)
	}
	sizes := make([]int, batch)
	for {
		n, err := d.dev.Read(bufs, sizes, tunOffset)
		if err != nil {
			if !d.closed.Load() {
				slog.Error("tun read failed", "error", err)
			}
			return
		}
		for i := 0; i < n; i++ {
			pkt := bufs[i][tunOffset : tunOffset+sizes[i]]
			pn, ok := protoOf(pkt)
			if !ok {
				continue
			}
			if d.ns.captureEcho(pkt) {
				continue
			}
			pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
				Payload: buffer.MakeWithData(pkt),
			})
			d.ns.ep.InjectInbound(pn, pb)
			pb.DecRef()
		}
	}
}

// stackToTun reads packets the netstack emits and writes them to the device
// in batches, so the device can coalesce the netstack's segments into one
// kernel write.
func (d *DataPlane) stackToTun(ctx context.Context) {
	batch := d.dev.BatchSize()
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, tunOffset+header.UDPMaximumPacketSize)
	}
	out := make([][]byte, 0, batch)
	for {
		pb := d.ns.ep.ReadContext(ctx)
		if pb == nil {
			return
		}
		out = out[:0]
		n := 0
		for {
			view := pb.ToView()
			size := copy(bufs[n][tunOffset:], view.AsSlice())
			view.Release()
			pb.DecRef()
			out = append(out, bufs[n][:tunOffset+size])
			n++
			if n == batch {
				break
			}
			pb = d.ns.ep.Read()
			if pb == nil {
				break
			}
		}
		if _, err := d.dev.Write(out, tunOffset); err != nil {
			if !d.closed.Load() {
				slog.Error("tun write failed", "error", err)
			}
			return
		}
	}
}

// protoOf reads the IP version nibble to pick the network protocol.
func protoOf(pkt []byte) (tcpip.NetworkProtocolNumber, bool) {
	if len(pkt) < 1 {
		return 0, false
	}
	switch pkt[0] >> 4 {
	case 4:
		return header.IPv4ProtocolNumber, true
	case 6:
		return header.IPv6ProtocolNumber, true
	default:
		return 0, false
	}
}
