package e2e

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"
)

func TestHelmScaleUp(t *testing.T) {
	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}
	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")
	username := "admin"
	password := "admin"

	if !repoPres {
		imageRepo = "marklogicdb/marklogic-db"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "latest"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	namespaceName := "ml-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues: map[string]string{
			"persistence.enabled":   "false",
			"replicaCount":          "1",
			"image.repository":      imageRepo,
			"image.tag":             imageTag,
			"auth.adminUsername":    username,
			"auth.adminPassword":    password,
			"logCollection.enabled": "false",
		},
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)
	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	t.Logf("====Installing Helm Chart")
	releaseName := "test-scale-up"
	helm.Install(t, options, helmChartPath, releaseName)

	newOptions := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues: map[string]string{
			"persistence.enabled":   "false",
			"replicaCount":          "2",
			"image.repository":      imageRepo,
			"image.tag":             imageTag,
			"logCollection.enabled": "false",
		},
	}
	podZeroName := releaseName + "-0"

	// wait until second pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podZeroName, 30, 10*time.Second)

	t.Logf("====Upgrading Helm Chart")
	helm.Upgrade(t, newOptions, helmChartPath, releaseName)

	podOneName := releaseName + "-1"

	// wait until second pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podOneName, 30, 10*time.Second)

	tunnel := k8s.NewTunnel(
		kubectlOptions, k8s.ResourceTypePod, podZeroName, 8002, 8002)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	numOfHosts := 1
	client := req.C()
	_, err := client.R().
		SetDigestAuth(username, password).
		SetRetryCount(3).
		SetRetryFixedInterval(10 * time.Second).
		AddRetryCondition(func(resp *req.Response, err error) bool {
			if resp == nil || err != nil {
				t.Logf("error in AddRetryCondition: %s", err.Error())
				return true
			}
			if resp.Response == nil {
				t.Log("Could not get the Response Object, Retrying...")
				return true
			}
			if resp.Body == nil {
				t.Log("Could not get the body for the response, Retrying...")
				return true
			}
			body, err := io.ReadAll(resp.Body)
			if body == nil || err != nil {
				t.Logf("error in read response body: %s", err.Error())
				return true
			}
			totalHosts := gjson.Get(string(body), `host-status-list.status-list-summary.total-hosts.value`)
			numOfHosts = int(totalHosts.Num)
			if numOfHosts != 2 {
				t.Log("Waiting for second host to join MarkLogic cluster")
			}
			return numOfHosts != 2
		}).
		Get("http://localhost:8002/manage/v2/hosts?view=status&format=json")

	if err != nil {
		t.Fatalf(err.Error())
	}
	// verify total number of hosts on the clsuter after scaling up
	if numOfHosts != 2 {
		t.Errorf("Incorrect number of MarkLogic hosts")
	}
}
