package controller

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/pipeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("Inventory watches", func() {
	It("grows, respects pause, watches replicas, and reloads every pool", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("KUBEBUILDER_ASSETS is unset")
		}
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "inventory"}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}})).To(Succeed())
		initial := testConfig()
		initial.Interval = time.Hour
		store := config.NewStore(initial)
		reload := make(chan event.GenericEvent, 10)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Cache: CacheOptions("inventory"), Client: client.Options{Cache: &client.CacheOptions{Unstructured: true}}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
		Expect(err).NotTo(HaveOccurred())
		installer := &installFake{notify: make(chan pipeline.InstallRequest, 100)}
		r, err := New(Options{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: store, Sweeper: &sweepFake{}, Queues: &queuesFake{result: pipeline.PollerStatus{Activity: 1, Workflow: 1, Segment: 1}}, Installer: installer, Metrics: metrics.New(prometheus.NewRegistry()), Reload: reload})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { stop(); Eventually(done).Should(Receive(BeNil())) })
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())
		// Seed steady supply first; the NodePool then creates a precise deficit of four.
		for _, a := range []*unstructured.Unstructured{agent("one", "p"), agent("two", "p")} {
			status := a.Object["status"]
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			a.Object["status"] = status
			Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, infra("p"))).To(Succeed())
		Expect(k8sClient.Create(ctx, node("workers", "p", 4))).To(Succeed())
		Eventually(installer.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 4})))
		// Pause is persisted on the InfraEnv; subsequent NodePool events cannot order.
		ie := infra("p")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ie), ie)).To(Succeed())
		ie.SetAnnotations(map[string]string{kube.AnnotationPaused: "true"})
		Expect(k8sClient.Update(ctx, ie)).To(Succeed())
		Eventually(func() string {
			u := infra("p")
			if mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(u), u) != nil {
				return ""
			}
			return u.GetAnnotations()[kube.AnnotationPaused]
		}).Should(Equal("true"))
		for len(installer.notify) > 0 {
			<-installer.notify
		}
		np := node("workers", "p", 4)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(7), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Consistently(installer.notify, 300*time.Millisecond).ShouldNot(Receive())
		// Unpause through config/annotation, then prove the NodePool watch itself fires.
		ie.SetAnnotations(nil)
		Expect(k8sClient.Update(ctx, ie)).To(Succeed())
		Eventually(installer.notify).Should(Receive())
		for len(installer.notify) > 0 {
			<-installer.notify
		}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(8), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Eventually(installer.notify).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 8})))
		Consistently(installer.notify, 200*time.Millisecond).ShouldNot(Receive())
		reload <- event.GenericEvent{Object: infra("p")}
		Eventually(installer.notify).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 8})))
	})
})
