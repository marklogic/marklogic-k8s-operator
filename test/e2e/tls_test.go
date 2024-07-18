package e2e

import (
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/marklogic/marklogic-kubernetes/test/testUtil"
	"github.com/tidwall/gjson"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
)

func TestTLSEnabledWithSelfSigned(t *testing.T) {
	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}
	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")
	var initialChartVersion string
	upgradeHelm, _ := os.LookupEnv("upgradeTest")
	runUpgradeTest, _ := strconv.ParseBool(upgradeHelm)
	if runUpgradeTest {
		initialChartVersion, _ = os.LookupEnv("initialChartVersion")
		t.Logf("====Setting initial Helm chart version: %s", initialChartVersion)
	}
	username := "admin"
	password := "admin"

	if !repoPres {
		imageRepo = "progressofficial/marklogic-db"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "latest"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}

	namespaceName := "marklogic-" + strings.ToLower(random.UniqueId())
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	valuesMap := map[string]string{
		"persistence.enabled":           "true",
		"replicaCount":                  "1",
		"image.repository":              imageRepo,
		"image.tag":                     imageTag,
		"auth.adminUsername":            username,
		"auth.adminPassword":            password,
		"logCollection.enabled":         "false",
		"tls.enableOnDefaultAppServers": "true",
	}
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
		SetValues:      valuesMap,
		Version:        initialChartVersion,
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	t.Logf("====Installing Helm Chart")
	releaseName := "test-join"
	//add the helm chart repo and install the last helm chart release from repository
	//to test and upgrade this chart to the latest one to be released
	if runUpgradeTest {
		delete(valuesMap, "image.repository")
		delete(valuesMap, "image.tag")
		helm.AddRepo(t, options, "marklogic", "https://marklogic.github.io/marklogic-kubernetes/")
		defer helm.RemoveRepo(t, options, "marklogic")
		helmChartPath = "marklogic/marklogic"
	}

	t.Logf("====Setting helm chart path to %s", helmChartPath)
	t.Logf("====Installing Helm Chart")
	podName := testUtil.HelmInstall(t, options, releaseName, kubectlOptions, helmChartPath)
	tlsConfig := tls.Config{InsecureSkipVerify: true}

	// wait until the pod is in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, 10, 20*time.Second)

	// verify MarkLogic is ready
	_, err := testUtil.MLReadyCheck(t, kubectlOptions, podName, &tlsConfig)
	if err != nil {
		t.Fatal("MarkLogic failed to start")
	}

	if runUpgradeTest {
		upgradeOptionsMap := map[string]string{
			"persistence.enabled":           "true",
			"replicaCount":                  "1",
			"tls.enableOnDefaultAppServers": "true",
			"logCollection.enabled":         "false",
			"allowLongHostnames":            "true",
			"rootToRootlessUpgrade":         "true",
		}
		if strings.HasPrefix(initialChartVersion, "1.0") {
			podName = releaseName + "-marklogic-0"
			upgradeOptionsMap["useLegacyHostnames"] = "true"
		}
		//set helmOptions for upgrade
		helmUpgradeOptions := &helm.Options{
			KubectlOptions: kubectlOptions,
			SetValues:      upgradeOptionsMap,
		}
		t.Logf("UpgradeHelmTest is set to %s. Running helm upgrade test" + upgradeHelm)
		testUtil.HelmUpgrade(t, helmUpgradeOptions, releaseName, kubectlOptions, []string{podName}, initialChartVersion)
	}

	tunnel := k8s.NewTunnel(
		kubectlOptions, k8s.ResourceTypePod, podName, 8002, 8002)
	defer tunnel.Close()
	tunnel.ForwardPort(t)
	endpointManage := fmt.Sprintf("https://%s/manage/v2", tunnel.Endpoint())
	t.Logf(`Endpoint: %s`, endpointManage)

	client := req.C().EnableInsecureSkipVerify()

	resp, err := client.R().
		SetDigestAuth(username, password).
		Get("https://localhost:8002/manage/v2")

	if err != nil {
		t.Fatalf(err.Error())
	}

	fmt.Println("StatusCode: ", resp.GetStatusCode())

	// restart pod in the cluster and verify its ready and MarkLogic server is healthy
	testUtil.RestartPodAndVerify(t, false, []string{podName}, namespaceName, kubectlOptions, &tlsConfig)
}

func GenerateCACertificate(caPath string) error {
	var err error
	fmt.Println("====Generating CA Certificates")
	genKeyCmd := strings.Replace("openssl genrsa -out caPath/ca-private-key.pem 2048", "caPath", caPath, -1)
	genCACertCmd := strings.Replace("openssl req -new -x509 -days 3650 -key caPath/ca-private-key.pem -out caPath/cacert.pem -subj '/CN=TlsTest/C=US/ST=California/L=RedwoodCity/O=Progress/OU=MarkLogic'", "caPath", caPath, -1)
	rvariable := []string{genKeyCmd, genCACertCmd}
	for _, j := range rvariable {
		cmd := exec.Command("bash", "-c", j)
		err = cmd.Run()
	}
	return err
}

func GenerateCertificates(path string, caPath string) error {
	var err error
	fmt.Println("====Generating TLS Certificates")
	genTLSKeyCmd := strings.Replace("openssl genpkey -algorithm RSA -out path/tls.key", "path", path, -1)
	genCsrCmd := strings.Replace("openssl req -new -key path/tls.key -config path/server.cnf -out path/tls.csr", "path", path, -1)
	genCrtCmd := strings.Replace(strings.Replace("openssl x509 -req -CA caPath/cacert.pem -CAkey caPath/ca-private-key.pem -CAcreateserial -CAserial path/cacert.srl -in path/tls.csr -out path/tls.crt -days 365", "path", path, -1), "caPath", caPath, -1)
	rvariable := []string{genTLSKeyCmd, genCsrCmd, genCrtCmd}
	for _, j := range rvariable {
		cmd := exec.Command("bash", "-c", j)
		err = cmd.Run()
	}
	return err
}

func TestTLSEnabledWithNamedCert(t *testing.T) {
	// Path to the helm chart we will test
	releaseName := "marklogic"
	namespaceName := "marklogic-" + "tlsnamed"
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	var err error
	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")
	var initialChartVersion string
	upgradeHelm, _ := os.LookupEnv("upgradeTest")
	runUpgradeTest, _ := strconv.ParseBool(upgradeHelm)
	if runUpgradeTest {
		initialChartVersion, _ = os.LookupEnv("initialChartVersion")
		t.Logf("====Setting initial Helm chart version: %s", initialChartVersion)
	}
	if !repoPres {
		imageRepo = "progressofficial/marklogic-db"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "latest-11"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}
	valuesMap := map[string]string{
		"image.repository": imageRepo,
		"image.tag":        imageTag,
	}
	// Setup the args for helm install using custom values.yaml file
	options := &helm.Options{
		ValuesFiles:    []string{"../test_data/values/tls_twonode_values.yaml"},
		SetValues:      valuesMap,
		Version:        initialChartVersion,
		KubectlOptions: k8s.NewKubectlOptions("", "", namespaceName),
	}

	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	// generate CA certificates for pods
	err = GenerateCACertificate("../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	//generate certificates for pod zero
	err = GenerateCertificates("../test_data/pod_zero_certs", "../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	//generate certificates for pod one
	err = GenerateCertificates("../test_data/pod_one_certs", "../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	// create secret for ca certificate
	t.Logf("====Creating secret for ca certificate")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "ca-cert", "--from-file=../test_data/ca_certs/cacert.pem")

	// create secret for named certificate for pod-0
	t.Logf("====Creating secret for pod-0 certificate")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "marklogic-0-cert", "--from-file=../test_data/pod_zero_certs/tls.crt", "--from-file=../test_data/pod_zero_certs/tls.key")

	// create secret for named certificate for pod-1
	t.Logf("====Creating secret for pod-1 certificate")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "marklogic-1-cert", "--from-file=../test_data/pod_one_certs/tls.crt", "--from-file=../test_data/pod_one_certs/tls.key")

	t.Logf("====Setting helm chart path to %s", helmChartPath)
	t.Logf("====Installing Helm Chart")
	//add the helm chart repo and install the last helm chart release from repository
	//to test and upgrade this chart to the latest one to be released
	if runUpgradeTest {
		delete(valuesMap, "image.repository")
		delete(valuesMap, "image.tag")
		helm.AddRepo(t, options, "marklogic", "https://marklogic.github.io/marklogic-kubernetes/")
		defer helm.RemoveRepo(t, options, "marklogic")
		helmChartPath = "marklogic/marklogic"
	}

	podName := testUtil.HelmInstall(t, options, releaseName, kubectlOptions, helmChartPath)
	podOneName := releaseName + "-1"

	tlsConfig := tls.Config{InsecureSkipVerify: true}

	// wait until pods are in Ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, 15, 30*time.Second)
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podOneName, 15, 30*time.Second)

	if runUpgradeTest {
		upgradeOptionsMap := map[string]string{
			"allowLongHostnames":    "true",
			"rootToRootlessUpgrade": "true",
		}

		if strings.HasPrefix(initialChartVersion, "1.0") {
			podName = releaseName + "-marklogic-0"
			podOneName = releaseName + "-marklogic-1"
			upgradeOptionsMap["useLegacyHostnames"] = "true"
		}
		helmUpgradeOptions := &helm.Options{
			KubectlOptions: kubectlOptions,
			ValuesFiles:    []string{"../test_data/values/tls_twonode_values.yaml"},
			SetValues:      upgradeOptionsMap,
		}
		t.Logf("UpgradeHelmTest is set. Running helmupgrade test")
		testUtil.HelmUpgrade(t, helmUpgradeOptions, releaseName, kubectlOptions, []string{podName, podOneName}, initialChartVersion)
	}
	output, err := testUtil.WaitUntilPodRunning(t, kubectlOptions, podName, 10, 15*time.Second)
	if err != nil {
		t.Error(err.Error())
	}
	if output != "Running" {
		t.Error(output)
	}
	// verify MarkLogic is ready
	_, err = testUtil.MLReadyCheck(t, kubectlOptions, podName, &tlsConfig)
	if err != nil {
		t.Fatal("MarkLogic failed to start")
	}

	tunnel := k8s.NewTunnel(
		kubectlOptions, k8s.ResourceTypePod, podName, 8002, 8002)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	totalHosts := 1
	client := req.C().
		EnableInsecureSkipVerify().
		SetCommonDigestAuth("admin", "admin").
		SetCommonRetryCount(10).
		SetCommonRetryFixedInterval(10 * time.Second)

	resp, err := client.R().
		AddRetryCondition(func(resp *req.Response, err error) bool {
			if err != nil {
				t.Logf("error in getting the response: %s", err.Error())
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Logf("error in reading the response: %s", err.Error())
			}
			totalHosts = int(gjson.Get(string(body), `host-status-list.status-list-summary.total-hosts.value`).Num)
			if totalHosts != 2 {
				t.Log("Waiting for second host to join MarkLogic cluster")
			}
			return totalHosts != 2
		}).
		Get("https://localhost:8002/manage/v2/hosts?view=status&format=json")
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer resp.Body.Close()
	if totalHosts != 2 {
		t.Errorf("Incorrect number of MarkLogic hosts")
	}

	resp, _ = client.R().
		Get("https://localhost:8002/manage/v2/certificate-templates/defaultTemplate?format=json")
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf(err.Error())
	}
	defaultCertTemplID := gjson.Get(string(body), `certificate-template-default.id`)

	resp, _ = client.R().
		Get("https://localhost:8002/manage/v2/certificates?format=json")
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf(err.Error())
	}
	certID := (gjson.Get(string(body), `certificate-default-list.list-items.list-item.1.idref`))

	endpoint := strings.Replace("https://localhost:8002/manage/v2/certificates/certId?format=json", "certId", certID.Str, -1)
	resp, _ = client.R().
		Get(endpoint)
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf(err.Error())
	}

	certTemplID := gjson.Get(string(body), `certificate-default.template-id`)
	isCertTemporary := gjson.Get(string(body), `certificate-default.temporary`)
	certHostName := gjson.Get(string(body), `certificate-default.host-name`)

	//verify named certificate is configured for default certificate template
	if defaultCertTemplID.Str != certTemplID.Str {
		t.Errorf("Named certificates not configured for defaultTemplate")
	}

	//verify temporary certificate is not used
	if isCertTemporary.Str != "false" {
		t.Errorf("Named certificate is not configured for host")
	}

	//verify correct hostname is set for named certificate
	t.Log("Verifying hostname is set for named certificate", certHostName.Str)

	if certHostName.Str != "marklogic-1.marklogic.marklogic-tlsnamed.svc.cluster.local" && certHostName.Str != "marklogic-0.marklogic.marklogic-tlsnamed.svc.cluster.local" {
		t.Errorf("Incorrect hostname configured for Named certificate")
	}

	// restart all pods at once in the cluster and verify its ready and MarkLogic server is healthy
	testUtil.RestartPodAndVerify(t, true, []string{podName, podOneName}, namespaceName, kubectlOptions, &tlsConfig)
}

func TestTlsOnEDnode(t *testing.T) {

	imageRepo, repoPres := os.LookupEnv("dockerRepository")
	imageTag, tagPres := os.LookupEnv("dockerVersion")
	var err error
	var initialChartVersion string
	upgradeHelm, _ := os.LookupEnv("upgradeTest")
	runUpgradeTest, _ := strconv.ParseBool(upgradeHelm)
	if runUpgradeTest {
		initialChartVersion, _ = os.LookupEnv("initialChartVersion")
		t.Logf("====Setting initial Helm chart version: %s", initialChartVersion)
	}
	namespaceName := "marklogic-tlsednode"
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)
	dnodeReleaseName := "dnode"
	enodeReleaseName := "enode"
	enodePodName1 := enodeReleaseName + "-1"

	// Path to the helm chart we will test
	helmChartPath, e := filepath.Abs("../../charts")
	if e != nil {
		t.Fatalf(e.Error())
	}

	if !repoPres {
		imageRepo = "progressofficial/marklogic-db"
		t.Logf("No imageRepo variable present, setting to default value: " + imageRepo)
	}

	if !tagPres {
		imageTag = "latest-11"
		t.Logf("No imageTag variable present, setting to default value: " + imageTag)
	}
	dnodeValuesMap := map[string]string{
		"image.repository": imageRepo,
		"image.tag":        imageTag,
	}
	bootstrapHostStr := ""
	enodeValuesMap := map[string]string{
		"image.repository":  imageRepo,
		"image.tag":         imageTag,
		"bootstrapHostName": bootstrapHostStr,
	}

	// Setup the args for helm install using custom values.yaml file
	options := &helm.Options{
		ValuesFiles:    []string{"../test_data/values/tls_dnode_values.yaml"},
		SetValues:      dnodeValuesMap,
		Version:        initialChartVersion,
		KubectlOptions: k8s.NewKubectlOptions("", "", namespaceName),
	}

	t.Logf("====Creating namespace: " + namespaceName)
	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

	defer t.Logf("====Deleting namespace: " + namespaceName)
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	//add the helm chart repo and install the last helm chart release from repository
	//to test and upgrade this chart to the latest one to be released
	if runUpgradeTest {
		delete(dnodeValuesMap, "image.repository")
		delete(dnodeValuesMap, "image.tag")
		delete(enodeValuesMap, "image.repository")
		delete(enodeValuesMap, "image.tag")
		helm.AddRepo(t, options, "marklogic", "https://marklogic.github.io/marklogic-kubernetes/")
		defer helm.RemoveRepo(t, options, "marklogic")
		helmChartPath = "marklogic/marklogic"
	}

	// generate CA certificates for pods
	err = GenerateCACertificate("../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	//generate certificates for dnode pod zero
	err = GenerateCertificates("../test_data/dnode_zero_certs", "../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	t.Logf("====Creating secret for ca certificate")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "ca-cert", "--from-file=../test_data/ca_certs/cacert.pem")

	t.Logf("====Creating secret for pod-0 certificate")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "dnode-0-cert", "--from-file=../test_data/dnode_zero_certs/tls.crt", "--from-file=../test_data/dnode_zero_certs/tls.key")

	t.Logf("====Setting helm chart path to %s", helmChartPath)
	t.Logf("====Installing Helm Chart " + dnodeReleaseName)
	dnodePodName := testUtil.HelmInstall(t, options, dnodeReleaseName, kubectlOptions, helmChartPath)

	// wait until the pod is in ready status
	k8s.WaitUntilPodAvailable(t, kubectlOptions, dnodePodName, 10, 20*time.Second)
	output, err := testUtil.WaitUntilPodRunning(t, kubectlOptions, dnodePodName, 20, 20*time.Second)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if output != "Running" {
		t.Error(output)
	}
	bootstrapHostStr, _ = VerifyDnodeConfig(t, dnodePodName, kubectlOptions, "https")
	enodeValuesMap["bootstrapHostName"] = bootstrapHostStr
	t.Logf("Enode joining Bootstrap host: %s", enodeValuesMap["bootstrapHostName"])

	// Setup the args for helm install using custom values.yaml file
	enodeOptions := &helm.Options{
		ValuesFiles:    []string{"../test_data/values/tls_enode_values.yaml"},
		SetValues:      enodeValuesMap,
		Version:        initialChartVersion,
		KubectlOptions: k8s.NewKubectlOptions("", "", namespaceName),
	}

	//generate certificates for enode pod zero
	err = GenerateCertificates("../test_data/enode_zero_certs", "../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	//generate certificates for enode pod one
	err = GenerateCertificates("../test_data/enode_one_certs", "../test_data/ca_certs")
	if err != nil {
		t.Log("====Error: ", err)
	}

	t.Logf("====Creating secret for enode-0 certificates")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "enode-0-cert", "--from-file=../test_data/enode_zero_certs/tls.crt", "--from-file=../test_data/enode_zero_certs/tls.key")

	t.Logf("====Creating secret for enode-1 certificates")
	k8s.RunKubectl(t, kubectlOptions, "create", "secret", "generic", "enode-1-cert", "--from-file=../test_data/enode_one_certs/tls.crt", "--from-file=../test_data/enode_one_certs/tls.key")

	t.Logf("====Setting helm chart path to %s", helmChartPath)
	t.Logf("====Installing Helm Chart " + enodeReleaseName)
	enodePodName0 := testUtil.HelmInstall(t, enodeOptions, enodeReleaseName, kubectlOptions, helmChartPath)

	// wait until the first enode pod is in Ready status
	output, err = testUtil.WaitUntilPodRunning(t, kubectlOptions, enodePodName1, 20, 20*time.Second)
	if err != nil {
		t.Error(err.Error())
	}
	if output != "Running" {
		t.Error(output)
	}

	if runUpgradeTest {
		t.Logf("UpgradeHelmTest is enabled. Running helm upgrade test")
		dnodeUpgradeOptionsMap := map[string]string{
			"allowLongHostnames":    "true",
			"rootToRootlessUpgrade": "true",
		}
		enodeUpgradeOptionsMap := map[string]string{
			"allowLongHostnames":    "true",
			"rootToRootlessUpgrade": "true",
		}
		if strings.HasPrefix(initialChartVersion, "1.0") {
			dnodePodName = dnodeReleaseName + "-marklogic-0"
			enodePodName0 = enodeReleaseName + "-marklogic-0"
			enodePodName1 = enodeReleaseName + "-marklogic-1"
			dnodeUpgradeOptionsMap["useLegacyHostnames"] = "true"
			enodeUpgradeOptionsMap["useLegacyHostnames"] = "true"
		}
		dnodeHelmUpgradeOptions := &helm.Options{
			ValuesFiles:    []string{"../test_data/values/tls_dnode_values.yaml"},
			KubectlOptions: kubectlOptions,
			SetValues:      dnodeUpgradeOptionsMap,
		}
		testUtil.HelmUpgrade(t, dnodeHelmUpgradeOptions, dnodeReleaseName, kubectlOptions, []string{dnodePodName}, initialChartVersion)
		output, err = testUtil.WaitUntilPodRunning(t, kubectlOptions, dnodePodName, 20, 30*time.Second)
		if err != nil {
			t.Fatalf(err.Error())
		}
		if output != "Running" {
			t.Fatalf(output)
		}
		bootstrapHostStr, _ = VerifyDnodeConfig(t, dnodePodName, kubectlOptions, "https")

		enodeUpgradeOptionsMap["bootstrapHostName"] = bootstrapHostStr
		enodeHelmUpgradeOptions := &helm.Options{
			ValuesFiles:    []string{"../test_data/values/tls_enode_values.yaml"},
			KubectlOptions: kubectlOptions,
			SetValues:      enodeUpgradeOptionsMap,
		}
		testUtil.HelmUpgrade(t, enodeHelmUpgradeOptions, enodeReleaseName, kubectlOptions, []string{enodePodName0, enodePodName1}, initialChartVersion)
	}

	// wait until the enode pod is in Running status
	output, err = testUtil.WaitUntilPodRunning(t, kubectlOptions, enodePodName0, 20, 20*time.Second)
	if err != nil {
		t.Error(err.Error())
	}
	if output != "Running" {
		t.Error(output)
	}
	VerifyEnodeConfig(t, dnodePodName, kubectlOptions, "https")

	tlsConfig := tls.Config{}
	// restart all pods at once in the cluster and verify its ready and MarkLogic server is healthy
	testUtil.RestartPodAndVerify(t, true, []string{dnodePodName, enodePodName0, enodePodName1}, namespaceName, kubectlOptions, &tlsConfig)
}
