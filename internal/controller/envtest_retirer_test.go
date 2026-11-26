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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func createAgent(u *unstructured.Unstructured) {
	status := u.Object["status"]
	Expect(k8sClient.Create(ctx, u)).To(Succeed())
	u.Object["status"] = status
	Expect(k8sClient.Status().Update(ctx, u)).To(Succeed())
}

func labelOf(name string) func() string {
	return func() string {
		u := kube.NewUnstructured(kube.AgentGVK)
		if k8sClient.Get(ctx, client.ObjectKey{Namespace: "inventory", Name: name}, u) != nil {
			return ""
		}
		return u.GetLabels()[kube.LabelEverInstalled]
	}
}

func unbind(name string) {
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		u := kube.NewUnstructured(kube.AgentGVK)
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "inventory", Name: name}, u); err != nil {
			return err
		}
		unstructured.RemoveNestedField(u.Object, "spec", "clusterDeploymentName")
		return k8sClient.Update(ctx, u)
	})).To(Succeed())
}

var _ = Describe("Retiring used servers", func() {
	It("labels on bind, removes once released, and waits for every NodePool to exclude the label", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("KUBEBUILDER_ASSETS is unset")
		}
		for _, name := range []string{"inventory", "tenant"} {
			err := k8sClient.Create(ctx, &corev1.Namespace{Name: name})
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
		}
		withServer := func(u *unstructured.Unstructured, server string) *unstructured.Unstructured {
			labels := u.GetLabels()
			labels[kube.LabelBMH] = server
			u.SetLabels(labels)
			return u
		}
		deleteAfterSpec(infra("r"), guardedNode("workers-r", "r", 2), node("legacy-r", "r", 0),
			agent("used-1", "r"), agent("used-2", "r"))
		initial := testConfig()
		initial.Interval = 10 * time.Minute
		initial.Defaults.DryRunRemove = false
		store := config.NewStore(initial)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Cache:   CacheOptions("inventory"),
			Client:  client.Options{Cache: &client.CacheOptions{Unstructured: true}},
			Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())
		r, err := New(Options{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: store,
			Sweeper:   &sweepFake{now: time.Now},
			Queues:    &queuesFake{result: pipeline.PollerStatus{Activity: 1, Workflow: 1, Segment: 1, Remove: 1}},
			Installer: &installFake{}, Metrics: metrics.New(prometheus.NewRegistry()),
		})
		Expect(err).NotTo(HaveOccurred())
		runs := &runsFake{runs: make(map[string]pipeline.RemoveRun)}
		remover := &removerFake{runs: runs, result: pipeline.RemoveResult{HTTPCode: 202}, notify: make(chan pipeline.RemoveRequest, 10)}
		retirer, err := r.NewRetirer(RetireOptions{Runs: runs, Remover: remover})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		Expect(retirer.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { stop(); Eventually(done).Should(Receive(BeNil())) })
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

		Expect(k8sClient.Create(ctx, infra("r"))).To(Succeed())
		Expect(k8sClient.Create(ctx, guardedNode("workers-r", "r", 2))).To(Succeed())
		createAgent(withServer(bound(agent("used-1", "r")), "srv-1"))
		Eventually(labelOf("used-1")).Should(Equal(kube.EverInstalledValue), "labelled on bind")
		Consistently(remover.notify, time.Second, 100*time.Millisecond).ShouldNot(Receive(), "never removed while bound")

		unbind("used-1")
		Eventually(remover.notify, 10*time.Second).Should(Receive(Equal(pipeline.RemoveRequest{Agent: "used-1", Server: "srv-1"})))
		Consistently(remover.notify, 1500*time.Millisecond, 100*time.Millisecond).ShouldNot(Receive(), "asked once while the run is open")

		Expect(k8sClient.Create(ctx, node("legacy-r", "r", 0))).To(Succeed())
		createAgent(withServer(bound(agent("used-2", "r")), "srv-2"))
		Eventually(labelOf("used-2")).Should(Equal(kube.EverInstalledValue))
		unbind("used-2")
		Consistently(remover.notify, 1500*time.Millisecond, 100*time.Millisecond).ShouldNot(Receive(),
			"a NodePool without the exclusion could claim it")

		Expect(k8sClient.Delete(ctx, node("legacy-r", "r", 0))).To(Succeed())
		Eventually(remover.notify, 10*time.Second).Should(Receive(Equal(pipeline.RemoveRequest{Agent: "used-2", Server: "srv-2"})),
			"the NodePool watch re-evaluates retiring Agents")
	})
})
