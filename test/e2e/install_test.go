package e2e

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/marklogic/marklogic-kubernetes/test/testUtil"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
	digestAuth "github.com/xinsnake/go-http-digest-auth-client"
)

func TestHelmInstall(t *testing.T) {
	var resp *http.Response
	var body []byte
	var err error
	var podZeroName string
	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")

	if !repoPres {
		imageRepo = "marklogic-centos/marklogic-server-centos"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "10-internal"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	options := map[string]string{
		"persistence.enabled":   "true",
		"replicaCount":          "2",
		"image.repository":      imageRepo,
		"image.tag":             imageTag,
		"logCollection.enabled": "false",
	}
	t.Logf("====Installing Helm Chart")
	releaseName := "test-install"

	namespaceName := "ml-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	podZeroName = testUtil.HelmInstall(t, options, releaseName, kubectlOptions)
	podOneName := releaseName + "-1"
	tlsConfig := tls.Config{}

	// wait until the pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podZeroName, 15, 15*time.Second)

	// verify MarkLogic is ready
	_, err = testUtil.MLReadyCheck(t, kubectlOptions, podZeroName, &tlsConfig)
	if err != nil {
		t.Fatal("MarkLogic failed to start")
	}

	t.Log("====Testing Generated Random Password====")
	secretName := releaseName + "-admin"
	secret := k8s.GetSecret(t, kubectlOptions, secretName)
	passwordArr := secret.Data["password"]
	password := string(passwordArr[:])
	// the generated random password should have length of 10
	assert.Equal(t, 10, len(password))
	usernameArr := secret.Data["username"]
	username := string(usernameArr[:])
	// the random generated username should have length of 11"
	assert.Equal(t, 11, len(username))

	tunnel8002 := k8s.NewTunnel(kubectlOptions, k8s.ResourceTypePod, podZeroName, 8002, 8002)
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

	// restart pod by pod in the cluster and verify its ready and MarkLogic server is healthy
	testUtil.RestartPodAndVerify(t, false, []string{podZeroName, podOneName}, namespaceName, kubectlOptions, &tlsConfig)

	// restart all pods in the cluster and verify its ready and MarkLogic server is healthy
	testUtil.RestartPodAndVerify(t, true, []string{podZeroName, podOneName}, namespaceName, kubectlOptions, &tlsConfig)
}
