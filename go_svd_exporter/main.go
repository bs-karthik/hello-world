package main

/*
replicationState - 1 -Ready, 2 retry , 3- Waiting, 4- Binding, 5- Connecting, 6 - Onhold , 7 - Error log full, 0 - unknown
replicationIsQuiesced - 0 - False, 1 - True
*/
import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/go-ldap/ldap/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	NAME           = "SVDMetrics"
	VERSION        = "1.0.0"
	MONITOR_OU     = "cn=monitor"
	MONITOR_FILTER = "(objectclass=*)"
	REPLICA_FILTER = "(&(objectClass=ibm-replicationagreement)(ibm-replicationLastChangeId=*))"
)

var (
	MONITOR_ATTRS = []string{
		"available_workers", "bindscompleted", "bindsrequested", "livethreads",
		"currentconnections", "largest_workqueue_size", "maxconnections", "total_ssl_connections",
		"total_tls_connections", "totalconnections", "deletescompleted", "deletesrequested",
		"comparescompleted", "comparesrequested", "extopscompleted", "extopsrequested",
		"modifiescompleted", "modifiesrequested", "modrdnscompleted", "modrdnsrequested",
		"searchescompleted", "searchesrequested", "unbindcompleted", "unbindsrequested",
	}
	REPLICA_ATTRS = []string{
		"ibm-replicationFailedChangeCount", "entryDN", "ibm-replicationPendingChangeCount",
		"ibm-replicationState", "ibm-replicationLastActivationTime", "ibm-replicationIsQuiesced",
		"ibm-replicationLastChangeId", "ibm-replicaconsumerid",
	}
	ldapURL      = getLDAPHost()
	ldapBindDN   = os.Getenv("LDAP_BINDDN")
	ldapPassword = os.Getenv("LDAP_BINDPWD")
	ldapConnPool *sync.Pool
)

// LdapMetricsCollector collects LDAP metrics.
type LdapMetricsCollector struct {
	monitorMetric     *prometheus.Desc
	replicationMetric *prometheus.Desc
}

func init() {
	ldapConnPool = &sync.Pool{
		New: func() interface{} {
			conn, err := connectLDAP()
			if err != nil {
				log.Fatalf("Failed to create LDAP connection: %v", err)
			}
			return conn
		},
	}
}

func connectLDAP() (*ldap.Conn, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // For testing; set to false in production
	}
	conn, err := ldap.DialURL(ldapURL, ldap.DialWithTLSConfig(tlsConfig))
	//conn, err := ldap.DialURL(ldapURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to LDAP on %s : %w", ldapURL, err)
	}

	if err := conn.Bind(ldapBindDN, ldapPassword); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to bind to LDAP: %w", err)
	}
	log.Printf("Connected to LDAP %s using %s ", ldapURL, ldapBindDN)
	return conn, nil
}
func getLDAPConnection() (*ldap.Conn, error) {
	conn := ldapConnPool.Get().(*ldap.Conn)
	if conn.IsClosing() {
		newConn, err := connectLDAP()
		if err != nil {
			return nil, err
		}
		return newConn, nil
	}
	return conn, nil
}

func releaseLDAPConnection(conn *ldap.Conn) {
	if conn.IsClosing() {
		log.Println("Releasing LDAP Connection to Pool")
		conn.Close()
	} else {
		ldapConnPool.Put(conn)
	}
}

// NewLdapMetricsCollector creates a new LdapMetricsCollector.
func SVDMetricsCollector() *LdapMetricsCollector {
	return &LdapMetricsCollector{
		monitorMetric: prometheus.NewDesc(
			"svd_monitor",
			"Exposes metrics from cn=monitor in LDAP",
			[]string{"attribute"}, nil,
		),
		replicationMetric: prometheus.NewDesc(
			"svd_replication",
			"Exposes replication metrics for suffix ",
			[]string{"consumerid", "suffix", "attribute"}, nil,
		),
	}
}

// Describe sends metric descriptions to the Prometheus channel.
func (collector *LdapMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.monitorMetric
	ch <- collector.replicationMetric
}

// Collect performs the LDAP search and sends metrics to the Prometheus channel.
func (collector *LdapMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	log.Printf("Connect to ldap to retrieve metrics ")
	var wg sync.WaitGroup

	conn, err := getLDAPConnection()
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}
	defer releaseLDAPConnection(conn)

	wg.Add(1)
	go func() {
		defer wg.Done()
		collector.collectMonitorMetrics(conn, ch)
	}()

	suffixes := []string{"DC=SYSTEMONE.COM", "SECAUTHORITY=DEFAULT", "CN=IBMPOLICIES"}
	for _, suffix := range suffixes {
		wg.Add(1)
		go func(suffix string) {
			defer wg.Done()
			collector.collectReplicationMetrics(conn, ch, suffix)
		}(suffix)
	}

	wg.Wait()
}

func (collector *LdapMetricsCollector) collectMonitorMetrics(conn *ldap.Conn, ch chan<- prometheus.Metric) {
	searchRequest := ldap.NewSearchRequest(
		MONITOR_OU, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false, MONITOR_FILTER, MONITOR_ATTRS, nil,
	)
	searchResult, err := conn.Search(searchRequest)
	if err != nil {
		log.Printf("Failed to search cn=monitor: %v", err)
		return
	}
	for _, entry := range searchResult.Entries {
		for _, attr := range entry.Attributes {
			for _, value := range attr.Values {
				floatValue, err := strconv.ParseFloat(value, 64)
				if err != nil {
					log.Printf("Failed to parse attribute %s value: %v", attr.Name, err)
					continue
				}
				ch <- prometheus.MustNewConstMetric(collector.monitorMetric, prometheus.GaugeValue, floatValue, attr.Name)
			}
		}
	}
}
func (collector *LdapMetricsCollector) collectReplicationMetrics(conn *ldap.Conn, ch chan<- prometheus.Metric, suffix string) {
	searchRequest := ldap.NewSearchRequest(
		suffix, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, REPLICA_FILTER, REPLICA_ATTRS, nil,
	)
	searchResult, err := conn.Search(searchRequest)
	if err != nil {
		log.Printf("Failed to search replication details for %s: %v", suffix, err)
		return
	}
	for _, entry := range searchResult.Entries {
		if entry.GetAttributeValue("ibm-replicationLastChangeId") == "" {
			//log.Printf("No replication attributes for suffix %s", suffix)
			continue
		}
		replMetrics := map[string]float64{
			"replicationState":             float64(getReplicationState(entry.GetAttributeValue("ibm-replicationState"))),
			"replicationIsQuiesced":        float64(getBooleanValue(entry.GetAttributeValue("ibm-replicationIsQuiesced"))),
			"replicationLastChangeId":      getFloatValue(entry.GetAttributeValue("ibm-replicationLastChangeId")),
			"replicationPendingCount":      getFloatValue(entry.GetAttributeValue("ibm-replicationPendingChangeCount")),
			"replicationFailedChangeCount": getFloatValue(entry.GetAttributeValue("ibm-replicationFailedChangeCount")),
		}
		consumerID := entry.GetAttributeValue("ibm-replicaconsumerid")
		for attr, value := range replMetrics {
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, value, consumerID, suffix, attr)
		}
	}
}
func getReplicationState(state string) int {
	switch state {
	case "ready":
		return 1
	case "retry":
		return 2
	case "waiting":
		return 3
	case "binding":
		return 4
	case "connecting":
		return 5
	case "onhold":
		return 6
	case "error log full":
		return 7
	default:
		return 0 // Unknown state
	}
}

func getBooleanValue(value string) int {
	retValue := 0
	if strings.EqualFold(value, "TRUE") {
		retValue = 1
	}
	return retValue
}
func getFloatValue(value string) float64 {
	floatValue, err := strconv.ParseFloat(value, 64)
	if err != nil {
		floatValue = -99999
	}
	return floatValue
}

func getLDAPHost() string {
	ldapURL := os.Getenv("LDAP_HOST")
	if ldapURL == "" {
		hostname, _ := os.Hostname()
		ldapURL = fmt.Sprintf("ldaps://%s:9636", hostname)
	}
	return ldapURL
}

func main() {

	if os.Getenv("LDAP_BINDDN") == "" || os.Getenv("LDAP_BINDPWD") == "" {
		log.Fatal("Missing Environment Variable LDAP_BINDDN LDAP_BINDPWD, Set the environment variable")
	}
	hostname, _ := os.Hostname()
	log.Printf("Collecting Metrics from %s using user %s and %s ", ldapURL, ldapBindDN, strings.Repeat("*", len(ldapPassword)))
	collector := SVDMetricsCollector()
	prometheus.MustRegister(collector)

	// Expose metrics endpoint
	http.Handle("/metrics", promhttp.Handler())
	fmt.Printf("Exposing metrics on http://%s:8080/metrics", hostname)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
