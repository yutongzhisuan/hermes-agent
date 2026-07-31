package client

import "github.com/prometheus/client_golang/prometheus"

func prometheusRegisterer() prometheus.Registerer {
	return prometheus.DefaultRegisterer
}
