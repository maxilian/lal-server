package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Publisher metrics
	PubBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
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

	// Subscriber metrics
	SubBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
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

	PubCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lal_publishers",
			Help: "Number of active publishers",
		},
		[]string{"app", "stream"},
	)
)

func Init() {
	prometheus.MustRegister(PubBytes, PubBitrate, SubBytes, SubBitrate, SubCount, PubCount)
}

func Handler() http.Handler {
	return promhttp.Handler()
}
