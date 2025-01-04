package main

/*
replicationState - 1 -Ready, 2 retry , 3- Waiting, 4- Binding, 5- Connecting, 6 - Onhold , 7 - Error log full, 0 - unknown
replicationIsQuiesced - 0 - False, 1 - True
*/
import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

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

// (&(objectclass=ibm-repl*nt)(objectClass=ibm-replicationagreement))
var MONITOR_ATTRS = []string{"available_workers", "bindscompleted", "bindsrequested", "livethreads",
	"currentconnections", "largest_workqueue_size", "maxconnections", "total_ssl_connections",
	"total_tls_connections", "totalconnections", "deletescompleted", "deletesrequested", "comparescompleted", "comparesrequested",
	"extopscompleted", "extopsrequested", "modifiescompleted", "modifiesrequested", "modrdnscompleted", "modrdnsrequested",
	"searchescompleted", "searchesrequested", "unbindcompleted", "unbindsrequested"}
var REPLICA_ATTRS = []string{"ibm-replicationFailedChangeCount", "entryDN", "ibm-replicationPendingChangeCount",
	"ibm-replicationState", "ibm-replicationLastActivationTime", "ibm-replicationIsQuiesced", "ibm-replicationLastChangeId", "ibm-replicaconsumerid"}

var ldapURL = getLDAPHost()                  //"ldap://localhost:30389"       //os.Getenv("LDAP_HOST")
var ldapBindDN = os.Getenv("LDAP_BINDDN")    //"cn=isvaadmin,CN=IBMPOLICIES" //os.Getenv("LDAP_BINDDN")
var ldapPassword = os.Getenv("LDAP_BINDPWD") //"sss"                 //os.Getenv("LDAP_BINDPWD")

// LdapMetricsCollector collects LDAP metrics.
type LdapMetricsCollector struct {
	monitorMetric     *prometheus.Desc
	replicationMetric *prometheus.Desc
}

func getSVDConnection() (*ldap.Conn, error) {
	conn, err := ldap.DialURL(ldapURL)

	err = conn.Bind(ldapBindDN, ldapPassword)
	return conn, err
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
}

// Collect performs the LDAP search and sends metrics to the Prometheus channel.
func (collector *LdapMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	log.Printf("Connect to ldap to retrieve metrics ")
	conn, err := getSVDConnection()
	defer conn.Close()
	if err != nil {
		log.Printf("Failed to connect to LDAP: %v", err)
		return
	}
	getCNMonitorMetrics(conn, collector, ch)
	getReplicationDetails(conn, collector, ch, "DC=SYSTEMONE.COM")
	getReplicationDetails(conn, collector, ch, "SECAUTHORITY=DEFAULT")
	getReplicationDetails(conn, collector, ch, "CN=IBMPOLICIES")
}

func getReplicationDetails(conn *ldap.Conn, collector *LdapMetricsCollector, ch chan<- prometheus.Metric, suffix string) {
	searchRequest := ldap.NewSearchRequest(suffix, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, REPLICA_FILTER, REPLICA_ATTRS, nil)
	searchResult, err := conn.Search(searchRequest)
	if err != nil {
		log.Printf("LDAP search failed: %v", err)
		return
	}

	log.Printf("Search Result replication entries for %s  %s ", suffix, len(searchResult.Entries))
	for _, entry := range searchResult.Entries {
		//log.Printf("Attribute length for %s %d ", suffix, len(entry.Attributes))
		if entry.GetAttributeValue("ibm-replicationLastChangeId") != "" {
			replChangeID := getFloatValue(entry.GetAttributeValue("ibm-replicationLastChangeId"))
			replPendingCount := getFloatValue(entry.GetAttributeValue("ibm-replicationPendingChangeCount"))
			replFailedCount := getFloatValue(entry.GetAttributeValue("ibm-replicationFailedChangeCount"))
			replState := getReplErrorState(entry.GetAttributeValue("ibm-replicationState"))
			replIsQuiesced := getBooleanValue(entry.GetAttributeValue("ibm-replicationIsQuiesced"))
			replConsumerID := entry.GetAttributeValue("ibm-replicaconsumerid")
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, float64(replState), replConsumerID, suffix, "replicationState")
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, float64(replIsQuiesced), replConsumerID, suffix, "replicationIsQuiesced")
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, replChangeID, replConsumerID, suffix, "replicationLastChangeId")
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, replPendingCount, replConsumerID, suffix, "replicationPendingChangeCount")
			ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, replFailedCount, replConsumerID, suffix, "replicationFailedChangeCount")
		} else { //
			log.Printf("No Replication attributes missing for the suffix %s ", suffix)
			return
			// ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, -99999, "", suffix, "replicationState")
			// ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, -99999, "", suffix, "replicationIsQuiesced")
			// ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, -99999, "", suffix, "replicationLastChangeId")
			// ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, -99999, "", suffix, "replicationPendingChangeCount")
			// ch <- prometheus.MustNewConstMetric(collector.replicationMetric, prometheus.GaugeValue, -99999, "", suffix, "replicationFailedChangeCount")
		}
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
func getReplErrorState(state string) int {
	//log.Printf("Attrs %s", state)
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

func getCNMonitorMetrics(conn *ldap.Conn, collector *LdapMetricsCollector, ch chan<- prometheus.Metric) {
	// Search CN=Monitor OU for metrics
	searchRequest := ldap.NewSearchRequest(MONITOR_OU, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false, MONITOR_FILTER, MONITOR_ATTRS, nil)
	searchResult, err := conn.Search(searchRequest)
	if err != nil {
		log.Printf("LDAP search failed: %v", err)
		return
	}
	for _, entry := range searchResult.Entries {
		for _, attr := range entry.Attributes {
			for _, value := range attr.Values {
				// Convert value to float if possible
				floatValue, err := strconv.ParseFloat(value, 64)
				if err != nil {
					log.Printf("Failed to parse value for attribute %s: %v", attr.Name, err)
					continue
				}
				// Send the metric to Prometheus
				ch <- prometheus.MustNewConstMetric(collector.monitorMetric, prometheus.GaugeValue, floatValue, attr.Name)
			}
		}
	}
}
func getLDAPHost() string {
	ldapURL := os.Getenv("LDAP_HOST")
	if ldapURL == "" {
		hostname, _ := os.Hostname()
		ldapURL = fmt.Sprintf("ldap://%s:9389", hostname)
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
