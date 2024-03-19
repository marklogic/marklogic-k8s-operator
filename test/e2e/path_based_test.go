package e2e

import (
	"fmt"
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

func TestPathBasedRouting(t *testing.T) {
	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}
	username := "admin"
	password := "admin"

	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")

	if !repoPres {
		imageRepo = "ml-docker-db-dev-tierpoint.bed-artifactory.bedford.progress.com/marklogic/marklogic-server-centos"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "11.0.nightly-centos-1.0.2"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	namespaceName := "ml-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues: map[string]string{
			"persistence.enabled":       "false",
			"replicaCount":              "3",
			"image.repository":          imageRepo,
			"image.tag":                 imageTag,
			"auth.adminUsername":        username,
			"auth.adminPassword":        password,
			"logCollection.enabled":     "false",
			"haproxy.enabled":           "true",
			"haproxy.replicaCount":      "1",
			"haproxy.frontendPort":      "80",
			"haproxy.pathbased.enabled": "true",
		},
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	t.Logf("====Installing Helm Chart")
	releaseName := "test-path"
	helm.Install(t, options, helmChartPath, releaseName)

	podName := releaseName + "-2"
	svcName := releaseName + "-haproxy"

	// wait until the pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, 15, 20*time.Second)

	tunnel := k8s.NewTunnel(
		kubectlOptions, k8s.ResourceTypeService, svcName, 8080, 80)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	client := req.C().
		SetCommonBasicAuth(username, password).
		SetCommonRetryCount(15).
		SetCommonRetryFixedInterval(15 * time.Second)

	paths := [3]string{"adminUI", "manage/dashboard", "console/qconsole/"}
	// using loop to verify path based routing
	for i := 0; i < len(paths); i++ {
		endpoint := fmt.Sprintf("http://localhost:8080/%s", paths[i])
		t.Logf("Verifying path based routing using %s", endpoint)
		resp, err := client.R().
			AddRetryCondition(func(resp *req.Response, err error) bool {
				if err != nil {
					t.Logf("error: %s", err.Error())
				}
				if resp.GetStatusCode() != 200 {
					t.Log("Waiting for MarkLogic cluster to be ready")
				}
				return resp.GetStatusCode() != 200
			}).
			Get(endpoint)

		if err != nil {
			t.Errorf("Error routing to %s", paths[i])
			t.Fatalf(err.Error())
		}
		defer resp.Body.Close()
	}

	appServers := [3]string{"Admin", "Manage", "App-Services"}
	// using loop to verify authentication for all 3 AppServers
	for i := 0; i < len(appServers); i++ {
		endpoint := fmt.Sprintf("http://localhost:8080/manage/manage/v2/servers/%s/properties?group-id=Default&format=json", appServers[i])
		t.Logf("Endpoint for %s AppServer is %s", appServers[i], endpoint)
		resp, err := client.R().
			AddRetryCondition(func(resp *req.Response, err error) bool {
				if err != nil {
					t.Logf("error: %s", err.Error())
				}
				t.Logf("StatusCode: %d", resp.GetStatusCode())
				if resp.GetStatusCode() != 200 {
					t.Log("Waiting for MarkLogic cluster to be ready")
				}
				return resp.GetStatusCode() != 200
			}).
			Get(endpoint)

		if err != nil {
			t.Errorf("Error getting AppServer properties")
			t.Fatalf(err.Error())
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf(err.Error())
		}
		serverAuthentication := gjson.Get(string(body), `authentication`)
		t.Logf("serverAuthentication: %s", serverAuthentication)
		//verify basic authentication is configured for AppServer
		t.Logf("Verifying authentication for %s AppServer", appServers[i])
		if serverAuthentication.Str != "basic" {
			t.Errorf("basic authentication is not configured for %s AppServer", appServers[i])
		}
	}
}

func TestPathBasedRoutingWithTLS(t *testing.T) {
	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}
	username := "admin"
	password := "admin"

	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")

	if !repoPres {
		imageRepo = "ml-docker-db-dev-tierpoint.bed-artifactory.bedford.progress.com/marklogic/marklogic-server-centos"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "11.0.nightly-centos-1.0.2"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	namespaceName := "marklogic-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues: map[string]string{
			"persistence.enabled":           "false",
			"replicaCount":                  "3",
			"image.repository":              imageRepo,
			"image.tag":                     imageTag,
			"auth.adminUsername":            username,
			"auth.adminPassword":            password,
			"logCollection.enabled":         "false",
			"tls.enableOnDefaultAppServers": "true",
			"haproxy.enabled":               "true",
			"haproxy.replicaCount":          "1",
			"haproxy.frontendPort":          "80",
			"haproxy.pathbased.enabled":     "true",
		},
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	t.Logf("====Installing Helm Chart")
	releaseName := "test-pb-tls"
	helm.Install(t, options, helmChartPath, releaseName)

	podName := releaseName + "-2"
	svcName := releaseName + "-haproxy"

	// wait until the pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, 10, 20*time.Second)

	tunnel := k8s.NewTunnel(
		kubectlOptions, k8s.ResourceTypeService, svcName, 8080, 80)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	client := req.C().
		EnableInsecureSkipVerify().
		SetCommonBasicAuth(username, password).
		SetCommonRetryCount(15).
		SetCommonRetryFixedInterval(15 * time.Second)

	paths := [3]string{"adminUI", "manage/dashboard", "console/qconsole/"}
	// using loop to verify path based routing
	for i := 0; i < len(paths); i++ {
		endpoint := fmt.Sprintf("http://localhost:8080/%s", paths[i])
		t.Logf("Verifying path based routing using %s", endpoint)
		resp, err := client.R().
			AddRetryCondition(func(resp *req.Response, err error) bool {
				if err != nil {
					t.Logf("error: %s", err.Error())
				}
				if resp.GetStatusCode() != 200 {
					t.Log("Waiting for MarkLogic cluster to be ready")
				}
				return resp.GetStatusCode() != 200
			}).
			Get(endpoint)

		if err != nil {
			t.Errorf("Error routing to %s", paths[i])
			t.Fatalf(err.Error())
		}
		defer resp.Body.Close()
	}

	appServers := [3]string{"Admin", "Manage", "App-Services"}
	// using loop to verify authentication for all 3 AppServers
	for i := 0; i < len(appServers); i++ {
		endpoint := fmt.Sprintf("http://localhost:8080/manage/manage/v2/servers/%s/properties?group-id=Default&format=json", appServers[i])
		t.Logf("Endpoint for %s AppServer is %s", appServers[i], endpoint)
		resp, err := client.R().
			Get(endpoint)

		if err != nil {
			t.Errorf("Error getting AppServer properties")
			t.Fatalf(err.Error())
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf(err.Error())
		}

		serverAuthentication := gjson.Get(string(body), `authentication`)
		//verify basic authentication is configured for AppServer
		t.Logf("Verifying authentication for %s AppServer", appServers[i])
		if serverAuthentication.Str != "basic" {
			t.Errorf("basic authentication is not configured for %s AppServer", appServers[i])
		}

		sslAllowTLS := gjson.Get(string(body), `ssl-allow-tls`)
		//verify ssl is enabled for AppServer
		t.Logf("Verifying ssl for %s AppServer", appServers[i])
		if sslAllowTLS.Bool() != true {
			t.Errorf("ssl is not enabled for %s AppServer", appServers[i])
		}
	}
}
