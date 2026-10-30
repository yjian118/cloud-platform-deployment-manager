/* Copyright(c) 2026 Wind River Systems, Inc. */
package host

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gophercloud/gophercloud/starlingx/inventory/v1/hosts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Host state monitors", func() {
	const hostID = "6b5647b1-dee1-40c6-af57-cf0e8d7134aa"

	// hostHandler serves a single host with the given state.
	hostHandler := func(admin, oper string) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/ihosts/"+hostID, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"uuid": %q, "hostname": "controller-0",
				"administrative": %q, "operational": %q, "availability": "available"}`,
				hostID, admin, oper)
		})
		return mux
	}

	// clock is a controllable time source for the monitor.
	type clock struct{ t time.Time }

	Context("when waiting for out-of-service attributes on an unlocked host", func() {
		It("should keep waiting before the recheck interval", func() {
			server, client := newTestServiceClient(hostHandler(hosts.AdminUnlocked, hosts.OperEnabled))
			defer server.Close()

			c := &clock{t: time.Now()}
			body := NewOutOfServiceAttributesMonitor(newHostInstance("controller-0", "default", false, nil), hostID).
				MonitorBody.(*stateMonitor)
			body.now = func() time.Time { return c.t }

			stop, err := body.Run(client)
			Expect(err).ToNot(HaveOccurred())
			Expect(stop).To(BeFalse())

			c.t = c.t.Add(DefaultOutOfServiceRecheckInterval - time.Second)
			stop, err = body.Run(client)
			Expect(err).ToNot(HaveOccurred())
			Expect(stop).To(BeFalse())
		})

		It("should trigger a re-evaluation once the recheck interval has elapsed", func() {
			server, client := newTestServiceClient(hostHandler(hosts.AdminUnlocked, hosts.OperEnabled))
			defer server.Close()

			c := &clock{t: time.Now()}
			body := NewOutOfServiceAttributesMonitor(newHostInstance("controller-0", "default", false, nil), hostID).
				MonitorBody.(*stateMonitor)
			body.now = func() time.Time { return c.t }

			stop, err := body.Run(client)
			Expect(err).ToNot(HaveOccurred())
			Expect(stop).To(BeFalse())

			c.t = c.t.Add(DefaultOutOfServiceRecheckInterval)
			stop, err = body.Run(client)
			Expect(err).ToNot(HaveOccurred())
			Expect(stop).To(BeTrue())
			Expect(body.State()).To(ContainSubstring("re-evaluating"))
		})

		It("should trigger immediately once the host is locked", func() {
			server, client := newTestServiceClient(hostHandler(hosts.AdminLocked, hosts.OperDisabled))
			defer server.Close()

			body := NewOutOfServiceAttributesMonitor(newHostInstance("controller-0", "default", false, nil), hostID).
				MonitorBody.(*stateMonitor)

			stop, err := body.Run(client)
			Expect(err).ToNot(HaveOccurred())
			Expect(stop).To(BeTrue())
			Expect(body.State()).To(ContainSubstring("desired state has been reached"))
		})
	})

	Context("when waiting for a host to be locked", func() {
		It("should never re-evaluate before the host is locked", func() {
			server, client := newTestServiceClient(hostHandler(hosts.AdminUnlocked, hosts.OperEnabled))
			defer server.Close()

			c := &clock{t: time.Now()}
			body := NewLockedDisabledHostMonitor(newHostInstance("controller-0", "default", false, nil), hostID).
				MonitorBody.(*stateMonitor)
			body.now = func() time.Time { return c.t }

			for i := 0; i < 3; i++ {
				stop, err := body.Run(client)
				Expect(err).ToNot(HaveOccurred())
				Expect(stop).To(BeFalse())
				c.t = c.t.Add(time.Hour)
			}
		})
	})
})
