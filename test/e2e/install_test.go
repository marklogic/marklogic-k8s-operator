package e2e

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/helm"
	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
	digestAuth "github.com/xinsnake/go-http-digest-auth-client"
)

func TestHelmInstall(t *testing.T) {
	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}
	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")
	var resp *http.Response
	var body []byte
	var err error

	if !repoPres {
		imageRepo = "marklogic-centos/marklogic-server-centos"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "10-internal"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	namespaceName := "marklogic-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues: map[string]string{
			"persistence.enabled":   "false",
			"replicaCount":          "2",
			"image.repository":      imageRepo,
			"image.tag":             imageTag,
			"logCollection.enabled": "false",
		},
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	t.Logf("====Installing Helm Chart")
	releaseName := "test-install"
	helm.Install(t, options, helmChartPath, releaseName)

	tlsConfig := tls.Config{}
	podName := releaseName + "-marklogic-0"
	// wait until the pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, 10, 15*time.Second)
	tunnel7997 := k8s.NewTunnel(kubectlOptions, k8s.ResourceTypePod, podName, 7997, 7997)
	defer tunnel7997.Close()
	tunnel7997.ForwardPort(t)
	endpoint7997 := fmt.Sprintf("http://%s", tunnel7997.Endpoint())

	// verify if 7997 health check endpoint returns 200
	http_helper.HttpGetWithRetryWithCustomValidation(
		t,
		endpoint7997,
		&tlsConfig,
		10,
		15*time.Second,
		func(statusCode int, body string) bool {
			return statusCode == 200
		},
	)

	t.Log("====Testing Generated Random Password====")
	secretName := releaseName + "-marklogic-admin"
	secret := k8s.GetSecret(t, kubectlOptions, secretName)
	passwordArr := secret.Data["password"]
	password := string(passwordArr[:])
	// the generated random password should have length of 10
	assert.Equal(t, 10, len(password))
	usernameArr := secret.Data["username"]
	username := string(usernameArr[:])
	// the random generated username should have length of 11"
	assert.Equal(t, 11, len(username))

	tunnel8002 := k8s.NewTunnel(kubectlOptions, k8s.ResourceTypePod, podName, 8002, 8002)
	defer tunnel8002.Close()
	tunnel8002.ForwardPort(t)
	endpointManage := fmt.Sprintf("http://%s/manage/v2", tunnel8002.Endpoint())

	request := digestAuth.NewRequest(username, password, "GET", endpointManage, "")
	response, err := request.Execute()
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer response.Body.Close()
	// the generated password should be able to access the manage endpoint
	assert.Equal(t, 200, response.StatusCode)

	t.Log("====Verify xdqp-ssl-enabled is set to true by default")
	endpoint := fmt.Sprintf("http://%s/manage/v2/groups/Default/properties?format=json", tunnel8002.Endpoint())
	t.Logf(`Endpoint for group properties: %s`, endpoint)

	request = digestAuth.NewRequest(username, password, "GET", endpoint, "")
	resp, err = request.Execute()
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer resp.Body.Close()
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf(err.Error())
	}

	xdqpSSLEnabled := gjson.Get(string(body), `xdqp-ssl-enabled`)
	// verify xdqp-ssl-enabled is set to trues
	assert.Equal(t, true, xdqpSSLEnabled.Bool(), "xdqp-ssl-enabled should be set to true")

	t.Log("====Verify no groups beyond default were created/modified====")
	groupStatusEndpoint := fmt.Sprintf("http://%s/manage/v2/groups?format=json", tunnel8002.Endpoint())
	groupStatus := digestAuth.NewRequest(username, password, "GET", groupStatusEndpoint, "")
	t.Logf(`groupStatusEndpoint: %s`, groupStatusEndpoint)
	if resp, err = groupStatus.Execute(); err != nil {
		t.Fatalf(err.Error())
	}
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		t.Fatalf(err.Error())
	}
	groupQuantityJSON := gjson.Get(string(body), "group-default-list.list-items.list-count.value")

	if groupQuantityJSON.Num != 1 {
		t.Errorf("Only one group should exist, instead %v groups exist", groupQuantityJSON.Num)
	}

}
