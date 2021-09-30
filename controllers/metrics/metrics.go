package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	customMetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	successMetric = "success"
	failureMetric = "failure"
)

var (
	PlatformStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_platform_status",
			Help: "status of an installation of components and provider operators",
		},
		[]string{
			"platform",
			"status",
		},
	)
	DBaasRequestHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "dbaas_request_duration_seconds",
		Help: "Duration of upstream calls to provider operator/service endpoints",
	}, []string{"provider_name", "instance_type", "instance_name", "action", "outcome"})
	InventoryElapsedTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_inventory_creation_ready_seconds",
			Help: "Elapsed time from DBaaS provider inventory creation to sync ready",
		},
		[]string{
			"provider",
			"inventory",
			"namespace",
		},
	)
	InventoryStatusReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_inventory_status_ready",
			Help: "DBaaS provider inventory is sync ready",
		},
		[]string{
			"provider",
			"inventory",
			"namespace",
		},
	)
	InventoryStatusReason = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_inventory_status_reason",
			Help: "Inventory status reason",
		},
		[]string{
			"provider",
			"inventory",
			"namespace",
			"reason",
		},
	)
	ConnectionElapsedTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_connection_creation_ready_seconds",
			Help: "Elapsed time from DBaaS provider connection creation to ready",
		},
		[]string{
			"provider",
			"connection",
			"namespace",
		},
	)
	ConnectionStatusReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_connection_status_ready",
			Help: "DBaaS provider connection is ready for binding",
		},
		[]string{
			"provider",
			"connection",
			"namespace",
		},
	)
	ConnectionStatusReason = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbaas_connection_status_reason",
			Help: "Connection status reason",
		},
		[]string{
			"provider",
			"connection",
			"namespace",
			"reason",
		},
	)
)

var gaugeMetricsList = []*prometheus.GaugeVec{
	PlatformStatus,
	InventoryElapsedTime,
	InventoryStatusReady,
	InventoryStatusReason,
	ConnectionElapsedTime,
	ConnectionStatusReady,
	ConnectionStatusReason,
}

func init() {
	// Register custom metrics with the global prometheus registry
	for _, metric := range gaugeMetricsList {
		customMetrics.Registry.MustRegister(metric)
	}
	customMetrics.Registry.MustRegister(DBaasRequestHistogram)
}

/*
// SetPlatformStatus exposes dbaas_platform_status metric for each platform
func SetPlatformStatusMetric(platformName dbaasv1alpha1.PlatformsName, status dbaasv1alpha1.PlatformsInstlnStatus) {
	//PlatformStatus.Reset()
	if len(platformName) > 0 {
		PlatformStatus.With(prometheus.Labels{"platform": string(platformName), "status": string(status)}).Set(float64(1))
	}
}
*/

// NewExecution creates an Execution instance and starts the timer
func NewExecution(providerName string, instanceType string, instanceName string, action string) Execution {
	return Execution{
		begin:  time.Now(),
		labels: prometheus.Labels{"provider_name": providerName, "instance_type": instanceType, "instance_name": instanceName, "action": action},
	}
}

// Execution tracks state for an API execution for emitting metrics
type Execution struct {
	begin  time.Time
	labels prometheus.Labels
}

// Finish is used to log duration and success/failure
func (e *Execution) Finish(err error) {
	if err == nil {
		e.labels["outcome"] = successMetric
	} else {
		e.labels["outcome"] = failureMetric
	}
	duration := time.Since(e.begin)
	DBaasRequestHistogram.With(e.labels).Observe(duration.Seconds())
}

type ProviderReasonsCache struct {
	mu      sync.Mutex
	reasons *map[string](*map[string]bool)
}

func (c *ProviderReasonsCache) GetProviderReasons(provider string) *map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return (*c.reasons)[provider]
}

func (c *ProviderReasonsCache) SetProviderReason(provider, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reasons == nil {
		c.reasons = &map[string](*map[string]bool){}
	}
	reasons, ok := (*c.reasons)[provider]
	if ok {
		(*reasons)[reason] = true
	} else {
		(*c.reasons)[provider] = &map[string]bool{reason: true}
	}
}
