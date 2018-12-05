package main

import (
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/AdguardTeam/AdGuardHome/dnsfilter"
	"github.com/AdguardTeam/AdGuardHome/dnsforward"
	"gopkg.in/yaml.v2"
)

const (
	currentSchemaVersion = 1         // used for upgrading from old configs to new config
	dataDir              = "data"    // data storage
	filterDir            = "filters" // cache location for downloaded filters, it's under DataDir
)

// configuration is loaded from YAML
// field ordering is important -- yaml fields will mirror ordering from here
type configuration struct {
	ourConfigFilename string // Config filename (can be overriden via the command line arguments)
	ourBinaryDir      string // Location of our directory, used to protect against CWD being somewhere else

	BindHost  string        `yaml:"bind_host"`
	BindPort  int           `yaml:"bind_port"`
	AuthName  string        `yaml:"auth_name"`
	AuthPass  string        `yaml:"auth_pass"`
	Language  string        `yaml:"language"` // two-letter ISO 639-1 language code
	CoreDNS   coreDNSConfig `yaml:"coredns"`
	Filters   []filter      `yaml:"filters"`
	UserRules []string      `yaml:"user_rules,omitempty"`

	sync.RWMutex `yaml:"-"`

	SchemaVersion int `yaml:"schema_version"` // keeping last so that users will be less tempted to change it -- used when upgrading between versions
}

// field ordering is important -- yaml fields will mirror ordering from here
type coreDNSConfig struct {
	binaryFile string
	coreFile   string
	Port       int `yaml:"port"`

	dnsforward.FilteringConfig `yaml:",inline"`

	Pprof        string   `yaml:"-"`
	Cache        string   `yaml:"-"`
	Prometheus   string   `yaml:"-"`
	BootstrapDNS string   `yaml:"bootstrap_dns"`
	UpstreamDNS  []string `yaml:"upstream_dns"`
}

var defaultDNS = []string{"tls://1.1.1.1", "tls://1.0.0.1"}

// initialize to default values, will be changed later when reading config or parsing command line
var config = configuration{
	ourConfigFilename: "AdGuardHome.yaml",
	BindPort:          3000,
	BindHost:          "127.0.0.1",
	CoreDNS: coreDNSConfig{
		Port:       53,
		binaryFile: "coredns",  // only filename, no path
		coreFile:   "Corefile", // only filename, no path
		FilteringConfig: dnsforward.FilteringConfig{
			ProtectionEnabled:  true, // whether or not use any of dnsfilter features
			FilteringEnabled:   true, // whether or not use filter lists
			BlockedResponseTTL: 10,   // in seconds
			QueryLogEnabled:    true,
			Ratelimit:          20,
			RefuseAny:          true,
		},
		BootstrapDNS: "8.8.8.8:53",
		UpstreamDNS:  defaultDNS,
		Cache:        "cache",
		Prometheus:   "prometheus :9153",
	},
	Filters: []filter{
		{Filter: dnsfilter.Filter{ID: 1}, Enabled: true, URL: "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt", Name: "AdGuard Simplified Domain Names filter"},
		{Filter: dnsfilter.Filter{ID: 2}, Enabled: false, URL: "https://adaway.org/hosts.txt", Name: "AdAway"},
		{Filter: dnsfilter.Filter{ID: 3}, Enabled: false, URL: "https://hosts-file.net/ad_servers.txt", Name: "hpHosts - Ad and Tracking servers only"},
		{Filter: dnsfilter.Filter{ID: 4}, Enabled: false, URL: "http://www.malwaredomainlist.com/hostslist/hosts.txt", Name: "MalwareDomainList.com Hosts List"},
	},
}

// Loads configuration from the YAML file
func parseConfig() error {
	configFile := filepath.Join(config.ourBinaryDir, config.ourConfigFilename)
	log.Printf("Reading YAML file: %s", configFile)
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		// do nothing, file doesn't exist
		log.Printf("YAML file doesn't exist, skipping: %s", configFile)
		return nil
	}
	yamlFile, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Printf("Couldn't read config file: %s", err)
		return err
	}
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		log.Printf("Couldn't parse config file: %s", err)
		return err
	}

	// Deduplicate filters
	{
		i := 0 // output index, used for deletion later
		urls := map[string]bool{}
		for _, filter := range config.Filters {
			if _, ok := urls[filter.URL]; !ok {
				// we didn't see it before, keep it
				urls[filter.URL] = true // remember the URL
				config.Filters[i] = filter
				i++
			}
		}
		// all entries we want to keep are at front, delete the rest
		config.Filters = config.Filters[:i]
	}

	updateUniqueFilterID(config.Filters)

	return nil
}

// Saves configuration to the YAML file and also saves the user filter contents to a file
func (c *configuration) write() error {
	c.Lock()
	defer c.Unlock()
	configFile := filepath.Join(config.ourBinaryDir, config.ourConfigFilename)
	log.Printf("Writing YAML file: %s", configFile)
	yamlText, err := yaml.Marshal(&config)
	if err != nil {
		log.Printf("Couldn't generate YAML file: %s", err)
		return err
	}
	err = safeWriteFile(configFile, yamlText)
	if err != nil {
		log.Printf("Couldn't save YAML config: %s", err)
		return err
	}

	return nil
}

func writeAllConfigs() error {
	err := config.write()
	if err != nil {
		log.Printf("Couldn't write config: %s", err)
		return err
	}

	userFilter := userFilter()
	err = userFilter.save()
	if err != nil {
		log.Printf("Couldn't save the user filter: %s", err)
		return err
	}

	return nil
}
