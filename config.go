package main

import (
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
)

// For data from "config.yaml" file.
type MainCfgYAML struct {
	WDEInstallationFolder string `yaml:"WDEInstallationFolder"`
	CustomisationsFolder  string `yaml:"CustomisationsFolder"`
	Log                   struct {
		Folder  string `yaml:"Folder"`
		Name    string `yaml:"Name"`
		Verbose string `yaml:"Verbose"`
	} `yaml:"Log"`
	RedundantFiles []string `yaml:"RedundantFiles"`
}

// Extract configuration file and unmarshall collected data into config variable.
func ReadConfigFromYAMLFile(cfgFilePath string) (MainCfgYAML, error) {
	log.Println("[START   ] ReadConfigFromYAMLFile")
	file, err := os.Open(cfgFilePath)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	var mainConfig MainCfgYAML
	err = yaml.Unmarshal(data, &mainConfig)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	log.Println("[SUCCESS ] ReadConfigFromYAMLFile")
	return mainConfig, nil
}
