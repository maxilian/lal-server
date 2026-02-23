package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Gauges for live state
	PubBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_pub_bytes_total",
			Help: "Total bytes read from publishers",
		},
		[]string{"app", "stream"},
	)

	PubBitrate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_pub_bitrate_kbps",
			Help: "Current bitrate of publishers",
		},
		[]string{"app", "stream"},
	)

	SubBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_sub_bytes_total",
			Help: "Total bytes written to subscribers",
		},
		[]string{"app", "stream"},
	)

	SubBitrate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_sub_bitrate_kbps",
			Help: "Current bitrate of subscribers",
		},
		[]string{"app", "stream"},
	)

	SubCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_subscribers",
			Help: "Number of active subscribers",
		},
		[]string{"app", "stream"},
	)

	// Counter for events
	KickCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lal_kick_session_total",
			Help: "Number of sessions kicked",
		},
		[]string{"app", "stream", "session"},
	)
)

func Init() {
	prometheus.Unregister(collectors.NewGoCollector())
	prometheus.Unregister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	prometheus.MustRegister(PubBytes, PubBitrate, SubBytes, SubBitrate, SubCount, KickCount)
}

func Handler() http.Handler {
	return promhttp.Handler()
}
