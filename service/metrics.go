package service

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	PollTotal         *prometheus.CounterVec
	PollDuration      prometheus.Histogram
	DiscoveryDelay    prometheus.Histogram
	LastPollSuccess   prometheus.Gauge
	CommentPollTotal  *prometheus.CounterVec
	CommentFoundTotal prometheus.Counter
	DeliveryTotal     *prometheus.CounterVec
	DeliveryDuration  prometheus.Histogram
	OutboxDepth       prometheus.Gauge
	OldestOutboxAge   prometheus.Gauge
	AuthState         prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		PollTotal:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bili_notify_poll_requests_total", Help: "Bilibili polling requests."}, []string{"result"}),
		PollDuration:      prometheus.NewHistogram(prometheus.HistogramOpts{Name: "bili_notify_poll_duration_seconds", Help: "Duration of one UP poll."}),
		DiscoveryDelay:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "bili_notify_discovery_delay_seconds", Help: "Delay from publication to discovery.", Buckets: []float64{5, 10, 30, 60, 120, 300, 900}}),
		LastPollSuccess:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "bili_notify_last_poll_success_timestamp_seconds", Help: "Unix timestamp of the last successful poll."}),
		CommentPollTotal:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bili_notify_comment_poll_total", Help: "Comment-area poll attempts."}, []string{"result"}),
		CommentFoundTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "bili_notify_comment_found_total", Help: "UP replies discovered for notification."}),
		DeliveryTotal:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bili_notify_delivery_total", Help: "Notification delivery attempts."}, []string{"channel_type", "result"}),
		DeliveryDuration:  prometheus.NewHistogram(prometheus.HistogramOpts{Name: "bili_notify_delivery_duration_seconds", Help: "Notification delivery duration."}),
		OutboxDepth:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "bili_notify_outbox_depth", Help: "Number of pending or blocked deliveries."}),
		OldestOutboxAge:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "bili_notify_oldest_outbox_age_seconds", Help: "Age of the oldest delivery."}),
		AuthState:         prometheus.NewGauge(prometheus.GaugeOpts{Name: "bili_notify_auth_state", Help: "1 when the Bilibili session is valid."}),
	}
	reg.MustRegister(m.PollTotal, m.PollDuration, m.DiscoveryDelay, m.LastPollSuccess, m.CommentPollTotal, m.CommentFoundTotal, m.DeliveryTotal, m.DeliveryDuration, m.OutboxDepth, m.OldestOutboxAge, m.AuthState)
	return m
}
