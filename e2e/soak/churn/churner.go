// Package churn drives a steady, rate-limited stream of short-lived pods onto
// the soak cluster's churn node so NMA observes constant pod start/stop
// activity (EFS mounts, ENI IP allocation, projected tokens, network policy
// eBPF attach/detach).
package churn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"

	"github.com/aws/eks-node-monitoring-agent/e2e/soak/workload"
)

// Default churn settings: 120 pods/min that each live about a minute, so
// roughly 120 churn pods are alive on the churn node at any time.
const (
	DefaultPodsPerMinute = 120
	DefaultLifetime      = time.Minute
	// DefaultMaxInFlight caps the pods the churner tracks at once, so a
	// stalled node builds a bounded backlog instead of an unbounded one.
	DefaultMaxInFlight  = 200
	DefaultReapInterval = 5 * time.Second
)

// Config configures a Churner.
type Config struct {
	Namespace     string
	PodsPerMinute float64
	// Lifetime is how long a pod lives, measured from creation, before the
	// churner deletes it.
	Lifetime     time.Duration
	MaxInFlight  int
	ReapInterval time.Duration
	// Templates are stamped out round-robin. See workload.ChurnPodTemplates.
	Templates []corev1.Pod
}

// DefaultConfig returns the standard soak churn settings.
func DefaultConfig(namespace string, templates []corev1.Pod) Config {
	return Config{
		Namespace:     namespace,
		PodsPerMinute: DefaultPodsPerMinute,
		Lifetime:      DefaultLifetime,
		MaxInFlight:   DefaultMaxInFlight,
		ReapInterval:  DefaultReapInterval,
		Templates:     templates,
	}
}

func (c Config) validate() error {
	var errs []error
	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace is required"))
	}
	if c.PodsPerMinute <= 0 {
		errs = append(errs, errors.New("pods per minute must be positive"))
	}
	if c.Lifetime <= 0 {
		errs = append(errs, errors.New("lifetime must be positive"))
	}
	if c.MaxInFlight <= 0 {
		errs = append(errs, errors.New("max in-flight must be positive"))
	}
	if c.ReapInterval <= 0 {
		errs = append(errs, errors.New("reap interval must be positive"))
	}
	if len(c.Templates) == 0 {
		errs = append(errs, errors.New("at least one pod template is required"))
	}
	return errors.Join(errs...)
}

// Stats are cumulative churn counters, except StartLatencies.
type Stats struct {
	Created      uint64
	Deleted      uint64
	CreateErrors uint64
	DeleteErrors uint64
	// SkippedAtCapacity counts creations skipped because MaxInFlight pods
	// were already tracked.
	SkippedAtCapacity uint64
	// Failed counts pods that reached the Failed phase before their lifetime.
	Failed uint64
	// NeverReady counts pods deleted at the end of their lifetime without
	// ever becoming Ready.
	NeverReady uint64
	// InFlight is the number of pods currently tracked.
	InFlight int
	// StartLatencies are the creation-to-Ready times observed since the
	// previous Stats call.
	StartLatencies []time.Duration
}

type trackedPod struct {
	created time.Time
	ready   bool
}

// Churner creates churn pods at a fixed rate and deletes them once they reach
// their lifetime.
type Churner struct {
	client   kubernetes.Interface
	cfg      Config
	limiter  *rate.Limiter
	selector labels.Selector
	now      func() time.Time

	mu       sync.Mutex
	next     int
	inFlight map[string]*trackedPod
	stats    Stats
}

// New returns a Churner for cfg.
func New(client kubernetes.Interface, cfg Config) (*Churner, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Churner{
		client: client,
		cfg:    cfg,
		// a burst of 1 spreads creations evenly instead of in bursts.
		limiter: rate.NewLimiter(rate.Limit(cfg.PodsPerMinute/60), 1),
		selector: labels.SelectorFromSet(labels.Set{
			workload.PartOfLabel:    workload.PartOfValue,
			workload.ComponentLabel: workload.ChurnComponent,
		}),
		now:      time.Now,
		inFlight: make(map[string]*trackedPod),
	}, nil
}

// Run churns pods until ctx is cancelled. Churn pods left behind by a
// previous run are adopted and deleted once they reach their lifetime.
func (c *Churner) Run(ctx context.Context) error {
	if err := c.reap(ctx); err != nil {
		return fmt.Errorf("adopting existing churn pods: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(c.cfg.ReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// errors are transient API failures; the next tick retries.
				_ = c.reap(ctx)
			}
		}
	}()

	for {
		if err := c.limiter.Wait(ctx); err != nil {
			break
		}
		c.createOne(ctx)
	}
	wg.Wait()
	return nil
}

// Stats returns the current counters and drains the start latencies.
func (c *Churner) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.InFlight = len(c.inFlight)
	c.stats.StartLatencies = nil
	return s
}

// createOne creates the next pod in the round-robin, unless MaxInFlight pods
// are already tracked.
func (c *Churner) createOne(ctx context.Context) {
	c.mu.Lock()
	if len(c.inFlight) >= c.cfg.MaxInFlight {
		c.stats.SkippedAtCapacity++
		c.mu.Unlock()
		return
	}
	tpl := c.cfg.Templates[c.next%len(c.cfg.Templates)]
	c.next++
	c.mu.Unlock()

	pod := c.podFromTemplate(tpl)
	created, err := c.client.CoreV1().Pods(c.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.stats.CreateErrors++
		return
	}
	c.stats.Created++
	c.inFlight[created.Name] = &trackedPod{created: c.creationTime(created)}
}

func (c *Churner) podFromTemplate(tpl corev1.Pod) *corev1.Pod {
	pod := tpl.DeepCopy()
	pod.Name = fmt.Sprintf("churn-%s-%s", tpl.Name, utilrand.String(8))
	pod.Namespace = c.cfg.Namespace
	pod.ResourceVersion = ""
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[workload.PartOfLabel] = workload.PartOfValue
	pod.Labels[workload.ComponentLabel] = workload.ChurnComponent
	return pod
}

// reap reconciles tracked pods with the cluster: it records start latencies,
// adopts untracked churn pods, forgets pods that disappeared, and deletes
// pods that failed or reached their lifetime.
func (c *Churner) reap(ctx context.Context) error {
	list, err := c.client.CoreV1().Pods(c.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.selector.String()})
	if err != nil {
		return err
	}

	now := c.now()
	var toDelete []string
	c.mu.Lock()
	seen := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		seen[pod.Name] = true
		tracked, ok := c.inFlight[pod.Name]
		if !ok {
			tracked = &trackedPod{created: c.creationTime(pod)}
			c.inFlight[pod.Name] = tracked
		}
		if readyAt, isReady := readyTime(pod); isReady && !tracked.ready {
			tracked.ready = true
			if !pod.CreationTimestamp.IsZero() {
				c.stats.StartLatencies = append(c.stats.StartLatencies, readyAt.Sub(pod.CreationTimestamp.Time))
			}
		}
		switch {
		case pod.Status.Phase == corev1.PodFailed:
			c.stats.Failed++
			toDelete = append(toDelete, pod.Name)
		case now.Sub(tracked.created) >= c.cfg.Lifetime:
			if !tracked.ready {
				c.stats.NeverReady++
			}
			toDelete = append(toDelete, pod.Name)
		}
	}
	for name := range c.inFlight {
		if !seen[name] {
			delete(c.inFlight, name)
		}
	}
	c.mu.Unlock()

	var errs []error
	for _, name := range toDelete {
		err := c.client.CoreV1().Pods(c.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		c.mu.Lock()
		if err != nil && !apierrors.IsNotFound(err) {
			c.stats.DeleteErrors++
			errs = append(errs, err)
		} else {
			c.stats.Deleted++
			delete(c.inFlight, name)
		}
		c.mu.Unlock()
	}
	return errors.Join(errs...)
}

// creationTime prefers the server's timestamp and falls back to now.
func (c *Churner) creationTime(pod *corev1.Pod) time.Time {
	if !pod.CreationTimestamp.IsZero() {
		return pod.CreationTimestamp.Time
	}
	return c.now()
}

func readyTime(pod *corev1.Pod) (time.Time, bool) {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return cond.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}
