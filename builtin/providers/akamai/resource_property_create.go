package akamai

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/papi-v1"
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceProperty() *schema.Resource {
	return &schema.Resource{
		Create: resourcePropertyCreate,
		Read:   resourcePropertyRead,
		Update: resourcePropertyUpdate,
		Delete: resourcePropertyDelete,
		Exists: resourcePropertyExists,
		// Importer: &schema.ResourceImporter{
		// State: importRecord,
		// },
		Schema: akamaiPropertySchema,
	}
}

func resourcePropertyCreate(d *schema.ResourceData, meta interface{}) error {
	group, e := getGroup(d)
	if e != nil {
		return e
	}

	contract, e := getContract(d)
	if e != nil {
		return e
	}

	product, e := getProduct(d, contract)
	if e != nil {
		return e
	}

	cloneFrom, e := getCloneFrom(d, group, contract)
	if e != nil {
		return e
	}

	property, e := createProperty(contract, group, product, cloneFrom, d)
	if e != nil {
		return e
	}

	// The API now has data, so save the state
	d.Set("property_id", property.PropertyID)
	d.SetId(fmt.Sprintf("%s-%s-%s-%s", group.GroupID, contract.ContractID, product.ProductID, property.PropertyID))

	rules, e := property.GetRules()
	if e != nil {
		return e
	}

	cpCode, e := createCpCode(contract, group, product, d)
	if e != nil {
		return e
	}

	origin, e := createOrigin(d)
	if e != nil {
		return e
	}

	addStandardBehaviors(rules, cpCode, origin)

	// get rules from the TF config
	unmarshalRules(d, rules)

	e = rules.Save()
	if e != nil {
		if e == papi.ErrorMap[papi.ErrInvalidRules] && len(rules.Errors) > 0 {
			var msg string
			for _, v := range rules.Errors {
				msg = msg + fmt.Sprintf("\n Rule validation error: %s %s %s %s %s", v.Type, v.Title, v.Detail, v.Instance, v.BehaviorName)
			}
			return errors.New("Error - Invalid Property Rules" + msg)
		}
		return e
	}

	d.SetId(fmt.Sprintf("%s-%s-%s-%s", group.GroupID, contract.ContractID, product.ProductID, property.PropertyID))

	hostnameEdgeHostnameMap, err := createHostnames(contract, group, product, d)
	if err != nil {
		return err
	}

	edgeHostnames, err := setEdgeHostnames(property, hostnameEdgeHostnameMap)
	if err != nil {
		return err
	}

	d.Set("edge_hostname", edgeHostnames)
	d.SetId(fmt.Sprintf("%s-%s-%s-%s", group.GroupID, contract.ContractID, product.ProductID, property.PropertyID))

	activation, err := activateProperty(property, d)
	if err != nil {
		return err
	}

	go activation.PollStatus(property)
polling:
	for activation.Status != papi.StatusActive {
		select {
		case statusChanged := <-activation.StatusChange:
			log.Printf("[DEBUG] Property Status: %s\n", activation.Status)
			if statusChanged == false {
				break polling
			}
			continue polling
		case <-time.After(time.Minute * 90):
			log.Println("[DEBUG] Activation Timeout (90 minutes)")
			break polling
		}
	}

	log.Println("[DEBUG] Done")
	return nil
}

func createProperty(contract *papi.Contract, group *papi.Group, product *papi.Product, cloneFrom *papi.ClonePropertyFrom, d *schema.ResourceData) (*papi.Property, error) {
	log.Println("[DEBUG] Creating property")

	property, err := group.NewProperty(contract)
	if err != nil {
		return nil, err
	}

	property.ProductID = product.ProductID
	property.PropertyName = d.Get("name").(string)
	if cloneFrom != nil {
		property.CloneFrom = cloneFrom
	}

	if ruleFormat, ok := d.GetOk("rule_format"); ok {
		property.RuleFormat = ruleFormat.(string)
	} else {
		ruleFormats := papi.NewRuleFormats()
		property.RuleFormat, err = ruleFormats.GetLatest()
	}

	err = property.Save()
	if err != nil {
		return nil, err
	}

	log.Printf("[DEBUG] Property created: %s\n", property.PropertyID)
	return property, nil
}
