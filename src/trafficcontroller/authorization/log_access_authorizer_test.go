package authorization

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/loggregatorlib/loggertesthelper"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var accessTests = []struct {
	target         string
	authToken      string
	expectedResult bool
}{
	//Allowed domains
	{
		"myAppId",
		"bearer something",
		true,
	},
	//Not allowed stuff
	{
		"notMyAppId",
		"bearer something",
		false,
	},
	{
		"nonExistantAppId",
		"bearer something",
		false,
	},
}

func TestUserRoleAccessCombinations(t *testing.T) {
	startHTTPServer()
	for i, test := range accessTests {
		authorizer := NewLogAccessAuthorizer("http://localhost:9876", true)
		result := authorizer(test.authToken, test.target, loggertesthelper.Logger())
		if result != test.expectedResult {
			t.Errorf("Access combination %d failed.", i)
		}
	}
}

func TestWorksIfServerIsSSLWithoutValidCertAndSkipVerifyCertIsTrue(t *testing.T) {
	logger := loggertesthelper.Logger()
	startHTTPSServer(logger)
	authorizer := NewLogAccessAuthorizer("https://localhost:9877", true)
	result := authorizer("bearer something", "myAppId", logger)
	if result != true {
		t.Errorf("Could not connect to secure server.")
	}

	authorizer = NewLogAccessAuthorizer("https://localhost:9877", false)
	result = authorizer("bearer something", "myAppId", logger)
	if result != false {
		t.Errorf("Should not be able to connect to secure server with a self signed cert if SkipVerifyCert is false.")
	}
}

type handler struct{}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	re := regexp.MustCompile("^/v2/apps/([^/?]+)$")
	result := re.FindStringSubmatch(r.URL.Path)
	if len(result) != 2 {
		w.WriteHeader(500)
		return
	}

	switch result[1] {
	case "myAppId":
		w.Write([]byte("{}"))
	case "notMyAppId":
		w.WriteHeader(403)
	default:
		w.WriteHeader(404)
	}
}

func startHTTPServer() {
	startFakeCloudController := func() {
		http.ListenAndServe(":9876", &handler{})
	}

	go startFakeCloudController()
}

func startHTTPSServer(logger *gosteno.Logger) {
	generateCert(logger)
	startFakeCloudController := func() {
		http.ListenAndServeTLS(":9877", "cert.pem", "key.pem", &handler{})
	}

	go startFakeCloudController()
	<-time.After(300 * time.Millisecond)
}

func generateCert(logger *gosteno.Logger) {
	// see: http://golang.org/src/pkg/crypto/tls/generate_cert.go
	host := "localhost"
	validFrom := "Jan 1 15:04:05 2011"
	validFor := 10 * 365 * 24 * time.Hour
	isCA := true
	rsaBits := 1024

	if len(host) == 0 {
		logger.Fatalf("Missing required --host parameter")
	}

	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		logger.Fatalf("failed to generate private key: %s", err)
		panic(err)
	}

	var notBefore time.Time
	if len(validFrom) == 0 {
		notBefore = time.Now()
	} else {
		notBefore, err = time.Parse("Jan 2 15:04:05 2006", validFrom)
		if err != nil {
			logger.Fatalf("Failed to parse creation date: %s\n", err)
			panic(err)
		}
	}

	notAfter := notBefore.Add(validFor)

	// end of ASN.1 time
	endOfTime := time.Date(2049, 12, 31, 23, 59, 59, 0, time.UTC)
	if notAfter.After(endOfTime) {
		notAfter = endOfTime
	}

	template := x509.Certificate{
		SerialNumber: new(big.Int).SetInt64(0),
		Subject: pkix.Name{
			Organization: []string{"Loggregator TrafficController TEST"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	hosts := strings.Split(host, ",")
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	if isCA {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		logger.Fatalf("Failed to create certificate: %s", err)
		panic(err)
	}

	certOut, err := os.Create("cert.pem")
	if err != nil {
		logger.Fatalf("failed to open cert.pem for writing: %s", err)
		panic(err)
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()
	logger.Info("written cert.pem\n")

	keyOut, err := os.OpenFile("key.pem", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		logger.Fatalf("failed to open key.pem for writing: %s", err)
		panic(err)
	}
	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	keyOut.Close()
	logger.Info("written key.pem\n")
}
