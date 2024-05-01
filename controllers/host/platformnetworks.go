/* SPDX-License-Identifier: Apache-2.0 */
/* Copyright(c) 2024 Wind River Systems, Inc. */

package host

import (
	"context"
	"fmt"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/starlingx/inventory/v1/addresspools"
	"github.com/gophercloud/gophercloud/starlingx/inventory/v1/networkAddressPools"
	"github.com/gophercloud/gophercloud/starlingx/inventory/v1/networks"
	perrors "github.com/pkg/errors"
	starlingxv1 "github.com/wind-river/cloud-platform-deployment-manager/api/v1"
	utils "github.com/wind-river/cloud-platform-deployment-manager/common"
	"github.com/wind-river/cloud-platform-deployment-manager/controllers/common"
	cloudManager "github.com/wind-river/cloud-platform-deployment-manager/controllers/manager"
	v1info "github.com/wind-river/cloud-platform-deployment-manager/platform"
	"k8s.io/apimachinery/pkg/types"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

// makeRangeArray converts an array of range structs to an array of arrays where
// the inner array contains two elements.  The first element is the range start
// address and the second element is the range end address.  This is to align
// with the system API formatting which represents a pair as an array of two
// elements.
func makeRangeArray(ranges []starlingxv1.AllocationRange) [][]string {
	result := make([][]string, len(ranges))
	for index, r := range ranges {
		result[index] = []string{r.Start, r.End}
	}

	return result
}

// compareRangeArrays compares two range arrays and returns true if they are
// equal.
func compareRangeArrays(x, y [][]string) bool {
	if len(x) != len(y) {
		return false
	}

	count := 0
	for _, o := range x {
		for _, i := range y {
			if strings.EqualFold(o[0], i[0]) && strings.EqualFold(o[1], i[1]) {
				count++
			}
		}
	}

	return len(x) == count
}

func GetSystemAddrPool(client *gophercloud.ServiceClient, addrpool_instance *starlingxv1.AddressPool) (*addresspools.AddressPool, error) {
	var found_addrpool *addresspools.AddressPool
	addrpool_list, err := addresspools.ListAddressPools(client)
	if err != nil {
		logHost.Error(err, "failed to fetch addresspools from system")
		return nil, err
	}
	// Preferably fetch the addresspool using UUID.
	if addrpool_instance.Status.ID != nil {
		found_addrpool = utils.GetSystemAddrPoolByUUID(addrpool_list, *addrpool_instance.Status.ID)
		if found_addrpool != nil {
			return found_addrpool, nil
		}
	}

	found_addrpool = utils.GetSystemAddrPoolByName(addrpool_list, addrpool_instance.Name)

	return found_addrpool, nil
}

func GetSystemNetwork(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork) (*networks.Network, error) {
	var found_network *networks.Network
	network_list, err := networks.ListNetworks(client)
	if err != nil {
		logHost.Error(err, "failed to fetch networks from system")
		return nil, err
	}

	// Preferably fetch the network using UUID.
	if network_instance.Status.ID != nil {
		found_network = utils.GetSystemNetworkByUUID(network_list, *network_instance.Status.ID)
		if found_network != nil {
			return found_network, nil
		}
	}

	found_network = utils.GetSystemNetworkByName(network_list, network_instance.Name)

	return found_network, nil
}

// ValidateAddressPool validates the addresspool spec specific to the network it will be associated with.
// This is different from validations done in addresspool webhook which is more primitive in nature.
// Result of this validation determines if at all reconciliation request has to be requeued.
func (r *HostReconciler) ValidateAddressPool(
	network_instance *starlingxv1.PlatformNetwork,
	addrpool_instance *starlingxv1.AddressPool,
	system_info *cloudManager.SystemInfo) bool {

	spec := addrpool_instance.Spec
	if network_instance.Spec.Type == cloudManager.OAMNetworkType {
		if system_info.SystemType == cloudManager.SystemTypeAllInOne &&
			system_info.SystemMode == cloudManager.SystemModeSimplex {

			if spec.FloatingAddress == nil ||
				spec.Gateway == nil {

				msg := "The 'floatingAddress' and 'gateway' are mandatory parameters for oam address pool in AIO-SX."
				logHost.Info(msg)
				return false
			}
		} else {
			// Multinode system
			if spec.FloatingAddress == nil ||
				spec.Gateway == nil ||
				spec.Controller0Address == nil ||
				spec.Controller1Address == nil {

				msg := fmt.Sprintf(
					"The %s are mandatory parameters for oam address pool in multinode setup.",
					"'floatingAddress', 'gateway', 'controller0Address' and 'controller1Address'")
				logHost.Info(msg)
				return false
			}
		}
	} else if network_instance.Spec.Type == cloudManager.MgmtNetworkType ||
		network_instance.Spec.Type == cloudManager.ClusterHostNetworkType ||
		network_instance.Spec.Type == cloudManager.PXEBootNetworkType {

		if spec.FloatingAddress == nil ||
			spec.Controller0Address == nil ||
			spec.Controller1Address == nil {

			msg := fmt.Sprintf(
				"The %s are mandatory parameters for %s address pools.",
				"'floatingAddress', 'controller0Address' and 'controller1Address'",
				"management, cluster-host and pxeboot")
			logHost.Info(msg)
			return false
		}

		if network_instance.Spec.Type == cloudManager.PXEBootNetworkType && utils.IsIPv6(addrpool_instance.Spec.Subnet) {
			log_msg := fmt.Sprintf(
				"Network of type pxeboot only supports pool of family IPv4. AddressPool '%s' will not be reconciled.",
				addrpool_instance.Name)
			logHost.Info(log_msg)
			return false
		}
	}

	return true
}

func (r *HostReconciler) IsNetworkUpdateRequired(network_instance *starlingxv1.PlatformNetwork, system_network *networks.Network) (opts networks.NetworkOpts, result bool, uuid string) {
	var delta strings.Builder

	spec := network_instance.Spec

	if system_network == nil || (network_instance.Name != system_network.Name) {
		opts.Name = &network_instance.Name
		delta.WriteString(fmt.Sprintf("\t+Name: %s\n", *opts.Name))
		result = true
	}

	if system_network == nil || (spec.Type != system_network.Type) {
		opts.Type = &spec.Type
		delta.WriteString(fmt.Sprintf("\t+Type: %s\n", *opts.Type))
		result = true
	}

	if system_network == nil || (spec.Dynamic != system_network.Dynamic) {
		opts.Dynamic = &spec.Dynamic
		delta.WriteString(fmt.Sprintf("\t+Dynamic: %v\n", *opts.Dynamic))
		result = true
	}

	if system_network != nil {
		uuid = system_network.UUID
	}

	deltaString := delta.String()
	if deltaString != "" {
		deltaString = "\n" + strings.TrimSuffix(deltaString, "\n")
		logHost.Info(fmt.Sprintf("delta configuration:%s\n", deltaString))
	}

	network_instance.Status.Delta = deltaString
	err := r.Client.Status().Update(context.TODO(), network_instance)
	if err != nil {
		logHost.Error(err, fmt.Sprintf("failed to update '%s' platform network delta", network_instance.Name))
	}

	return opts, result, uuid
}

func (r *HostReconciler) IsAddrPoolUpdateRequired(network_instance *starlingxv1.PlatformNetwork, addrpool_instance *starlingxv1.AddressPool, system_addrpool *addresspools.AddressPool) (opts addresspools.AddressPoolOpts, result bool, uuid string) {
	var delta strings.Builder

	if system_addrpool == nil || (addrpool_instance.Name != system_addrpool.Name) {
		opts.Name = &addrpool_instance.Name
		delta.WriteString(fmt.Sprintf("\t+Name: %s\n", *opts.Name))
		result = true
	}

	spec := addrpool_instance.Spec

	if system_addrpool == nil || !utils.IsIPAddressSame(spec.Subnet, system_addrpool.Network) {
		opts.Network = &spec.Subnet
		delta.WriteString(fmt.Sprintf("\t+Network: %s\n", *opts.Network))
		result = true
	}

	if system_addrpool == nil || spec.Prefix != system_addrpool.Prefix {
		opts.Prefix = &spec.Prefix
		delta.WriteString(fmt.Sprintf("\t+Prefix: %d\n", *opts.Prefix))
		result = true
	}

	if (system_addrpool == nil && spec.FloatingAddress != nil) ||
		(spec.FloatingAddress != nil && !utils.IsIPAddressSame(*spec.FloatingAddress, system_addrpool.FloatingAddress)) {
		opts.FloatingAddress = spec.FloatingAddress
		delta.WriteString(fmt.Sprintf("\t+Floating Address: %s\n", *opts.FloatingAddress))
		result = true
	} else if spec.FloatingAddress == nil && system_addrpool != nil && system_addrpool.FloatingAddress != "" {
		opts.FloatingAddress = spec.FloatingAddress
		delta.WriteString(fmt.Sprintf("\t-Floating Address: %s\n", system_addrpool.FloatingAddress))
		result = true
	}

	if (system_addrpool == nil && spec.Controller0Address != nil) ||
		(spec.Controller0Address != nil && !utils.IsIPAddressSame(*spec.Controller0Address, system_addrpool.Controller0Address)) {
		opts.Controller0Address = spec.Controller0Address
		delta.WriteString(fmt.Sprintf("\t+Controller0 Address: %s\n", *opts.Controller0Address))
		result = true
	} else if spec.Controller0Address == nil && system_addrpool != nil && system_addrpool.Controller0Address != "" {
		opts.Controller0Address = spec.Controller0Address
		delta.WriteString(fmt.Sprintf("\t-Controller0 Address: %s\n", system_addrpool.Controller0Address))
		result = true
	}

	if (system_addrpool == nil && spec.Controller1Address != nil) ||
		(spec.Controller1Address != nil && !utils.IsIPAddressSame(*spec.Controller1Address, system_addrpool.Controller1Address)) {
		opts.Controller1Address = spec.Controller1Address
		delta.WriteString(fmt.Sprintf("\t+Controller1 Address: %s\n", *opts.Controller1Address))
		result = true
	} else if spec.Controller1Address == nil && system_addrpool != nil && system_addrpool.Controller1Address != "" {
		opts.Controller1Address = spec.Controller1Address
		delta.WriteString(fmt.Sprintf("\t-Controller1 Address: %s\n", system_addrpool.Controller1Address))
		result = true
	}

	if network_instance.Spec.Type != networks.NetworkTypeOther {
		// TODO(alegacy): There is a sysinv bug in how the gateway address
		//  gets registered in the database.  It doesn't have a "name" and
		//  so causes an exception when a related route is added.
		if (system_addrpool == nil && spec.Gateway != nil) ||
			(spec.Gateway != nil && system_addrpool.Gateway == nil) ||
			(spec.Gateway != nil && !utils.IsIPAddressSame(*spec.Gateway, *system_addrpool.Gateway)) {
			opts.Gateway = spec.Gateway
			delta.WriteString(fmt.Sprintf("\t+Gateway: %s\n", *opts.Gateway))
			result = true
		} else if spec.Gateway == nil && system_addrpool != nil && system_addrpool.Gateway != nil {
			opts.Gateway = spec.Gateway
			delta.WriteString(fmt.Sprintf("\t-Gateway Address: %s\n", *system_addrpool.Gateway))
			result = true
		}
	}

	if system_addrpool == nil || (spec.Allocation.Order != nil && *spec.Allocation.Order != system_addrpool.Order) {
		opts.Order = spec.Allocation.Order
		delta.WriteString(fmt.Sprintf("\t+Order: %s\n", *opts.Order))
		result = true
	}

	if len(spec.Allocation.Ranges) > 0 {
		ranges := makeRangeArray(spec.Allocation.Ranges)
		if system_addrpool == nil || !compareRangeArrays(ranges, system_addrpool.Ranges) {
			opts.Ranges = &ranges
			delta.WriteString(fmt.Sprintf("\t+Ranges: %s\n", *opts.Ranges))
			result = true
		}
	}

	if system_addrpool != nil {
		uuid = system_addrpool.ID
	}

	deltaString := delta.String()
	if deltaString != "" {
		deltaString = "\n" + strings.TrimSuffix(deltaString, "\n")
		logHost.Info(fmt.Sprintf("delta configuration:%s\n", deltaString))
	}

	addrpool_instance.Status.Delta = deltaString
	err := r.Client.Status().Update(context.TODO(), addrpool_instance)
	if err != nil {
		logHost.Error(err, fmt.Sprintf("failed to update '%s' addresspool delta", addrpool_instance.Name))
	}

	return opts, result, uuid
}

func (r *HostReconciler) ReconcileAddrPoolResource(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork, addrpool_instance *starlingxv1.AddressPool, system_info *cloudManager.SystemInfo) (error, *bool, *bool) {

	system_addrpool, err := GetSystemAddrPool(client, addrpool_instance)
	if err != nil {
		return err, nil, nil
	}

	r.UpdateAddrPoolUUID(addrpool_instance, system_addrpool)

	opts, update_required, uuid := r.IsAddrPoolUpdateRequired(network_instance, addrpool_instance, system_addrpool)

	err, should_reconcile := r.ShouldReconcile(client, network_instance, addrpool_instance, update_required, uuid)
	if err != nil {
		return err, nil, nil
	}

	validation_result := r.ValidateAddressPool(network_instance, addrpool_instance, system_info)

	if should_reconcile && validation_result && update_required {
		err := r.CreateOrUpdateAddrPools(client, opts, uuid, addrpool_instance)
		if err == nil {
			// Make sure network UUID is synchronized
			system_addrpool, err = GetSystemAddrPool(client, addrpool_instance)
			if err != nil {
				return err, nil, nil
			}
			r.UpdateAddrPoolUUID(addrpool_instance, system_addrpool)
		}
		return err, &should_reconcile, &validation_result
	} else if !validation_result {
		// These errors are to be corrected by the user.
		// No use requeuing the request until user corrects it.

		// Validation applies for addresspools to be created in the
		// context of network but not for addresspool that already exists
		// as per spec.
		validation_result = validation_result || !update_required
		return nil, &should_reconcile, &validation_result
	} else if update_required {
		msg := fmt.Sprintf(
			"There is delta between applied spec and system for addresspool '%s'",
			addrpool_instance.Name)
		logHost.Info(msg)
		err := perrors.New(msg)
		return err, &should_reconcile, &validation_result
	}

	return nil, &should_reconcile, &validation_result

}

// Synchronize AddressPoolStatus.ID with correct UUID
// of the addresspool as reported by the system.
func (r *HostReconciler) UpdateAddrPoolUUID(addrpool_instance *starlingxv1.AddressPool, system_addrpool *addresspools.AddressPool) {
	update_required := false
	if system_addrpool != nil {
		if addrpool_instance.Status.ID == nil {
			update_required = true
			addrpool_instance.Status.ID = &system_addrpool.ID
		} else {
			// Update stray UUID however this may have been caused.
			if *addrpool_instance.Status.ID != system_addrpool.ID {
				update_required = true
				addrpool_instance.Status.ID = &system_addrpool.ID
			}
		}
	}

	if update_required {
		err := r.Client.Status().Update(context.TODO(), addrpool_instance)
		if err != nil {
			// Logging the error should be enough, failure to update addrpool instance
			// UUID should not block rest of the reconciliation since we
			// always fallback to Name based addrpool instance lookup in case
			// UUID is not updated / not valid.
			logHost.Error(err, fmt.Sprintf("failed to update '%s' addresspool UUID", addrpool_instance.Name))
		}
	}
}

// Synchronize PlatformNetworkStatus.ID with correct UUID
// of the network as reported by the system.
func (r *HostReconciler) UpdateNetworkUUID(network_instance *starlingxv1.PlatformNetwork, system_network *networks.Network) {
	update_required := false
	if system_network != nil {
		if network_instance.Status.ID == nil {
			update_required = true
			network_instance.Status.ID = &system_network.UUID
		} else {
			// Update stray UUID however this may have been caused.
			if *network_instance.Status.ID != system_network.UUID {
				update_required = true
				network_instance.Status.ID = &system_network.UUID
			}
		}
	}

	if update_required {
		err := r.Client.Status().Update(context.TODO(), network_instance)
		if err != nil {
			// Logging the error should be enough, failure to update network
			// UUID should not block rest of the reconciliation since we
			// always fallback to Name based platform network lookup in case
			// UUID is not updated / not valid.
			logHost.Error(err, fmt.Sprintf("failed to update '%s' platform network UUID", network_instance.Name))
		}
	}
}

func (r *HostReconciler) IsReconfiguration(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork, addrpool_instance *starlingxv1.AddressPool) (error, bool) {
	err, system_network, system_network_addrpools, addrpool_list := r.GetAllNetworkAddressPoolData(
		client, network_instance)
	if err != nil {
		return err, false
	}

	if system_network != nil {
		network_addrpool, associated_addrpool := GetAssociatedNetworkAddrPool(
			system_network,
			addrpool_instance,
			system_network_addrpools,
			addrpool_list)

		if network_addrpool != nil && associated_addrpool != nil {
			// There exists an associated addresspool from same IP family.
			// This is an attempt to reconfigure the platform network.
			return nil, true
		}
	}

	return nil, false
}

// ShouldReconcile is a very important function that really controls the reconciliation
// behaviour of network and associated addresspools. Note that parameters 'update_required'
// and 'uuid' refers to address pool update_required and address pool uuid when called from
// ReconcileAddrPoolResource function.
func (r *HostReconciler) ShouldReconcile(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork, addrpool_instance *starlingxv1.AddressPool, update_required bool, uuid string) (error, bool) {
	if network_instance.Status.DeploymentScope == cloudManager.ScopeBootstrap {
		switch network_instance.Spec.Type {
		case cloudManager.OAMNetworkType,
			cloudManager.MgmtNetworkType,
			cloudManager.AdminNetworkType:
			// Block both fresh configuration / reconfiguration of networks / addrpools
			// such as oam / mgmt / admin in day-1.
			return nil, false
		default:
			// Allow fresh configuration of networks / addrpools other than
			// oam / mgmt / admin in day-1 but not reconfiguration.
			if addrpool_instance != nil {
				err, is_reconfig := r.IsReconfiguration(client, network_instance, addrpool_instance)
				if err != nil {
					return err, false
				}
				if !is_reconfig {
					return nil, true
				}
			} else {
				// for networks
				if uuid == "" {
					return nil, true
				}
			}

		}
	}

	// Unless explicitly specified that reconciliation is allowed
	// for given instances of platform network and address pools
	// return false.
	return nil, false
}

func (r *HostReconciler) CreateOrUpdateNetworks(client *gophercloud.ServiceClient, opts networks.NetworkOpts, uuid string, network_instance *starlingxv1.PlatformNetwork) error {
	if uuid == "" {
		_, err := networks.Create(client, opts).Extract()
		if err != nil {
			logHost.Error(err, fmt.Sprintf("failed to create platform network: %s", common.FormatStruct(opts)))
			return err
		}

		r.ReconcilerEventLogger.NormalEvent(network_instance, common.ResourceCreated,
			fmt.Sprintf("platform network '%s' has been created", *opts.Name))
	} else {
		_, err := networks.Update(client, uuid, opts).Extract()
		if err != nil {
			logHost.Error(err, fmt.Sprintf("failed to update platform network: %s", common.FormatStruct(opts)))
			return err
		}

		r.ReconcilerEventLogger.NormalEvent(network_instance, common.ResourceUpdated,
			fmt.Sprintf("platform network '%s' has been updated", *opts.Name))
	}

	return nil
}

func (r *HostReconciler) CreateOrUpdateAddrPools(client *gophercloud.ServiceClient, opts addresspools.AddressPoolOpts, uuid string, addrpool_instance *starlingxv1.AddressPool) error {
	if uuid == "" {
		_, err := addresspools.Create(client, opts).Extract()
		if err != nil {
			logHost.Error(err, fmt.Sprintf("failed to create addresspool: %s", common.FormatStruct(opts)))
			return err
		}

		r.ReconcilerEventLogger.NormalEvent(addrpool_instance, common.ResourceCreated,
			fmt.Sprintf("addresspool '%s' has been created", *opts.Name))
	} else {
		_, err := addresspools.Update(client, uuid, opts).Extract()
		if err != nil {
			logHost.Error(err, fmt.Sprintf("failed to update addresspool: %s", common.FormatStruct(opts)))
			return err
		}

		r.ReconcilerEventLogger.NormalEvent(addrpool_instance, common.ResourceUpdated,
			fmt.Sprintf("addresspool '%s' has been updated", *opts.Name))
	}

	return nil
}

func GetAssociatedNetworkAddrPool(
	system_network *networks.Network,
	addrpool_instance *starlingxv1.AddressPool,
	system_network_addrpools []networkAddressPools.NetworkAddressPool,
	addrpool_list []addresspools.AddressPool) (*networkAddressPools.NetworkAddressPool, *addresspools.AddressPool) {

	for _, network_addrpool := range system_network_addrpools {
		if network_addrpool.NetworkUUID == system_network.UUID {
			addrpool := utils.GetSystemAddrPoolByUUID(addrpool_list, network_addrpool.AddressPoolUUID)
			if addrpool != nil {
				if utils.IsIPv4(addrpool.Network) == utils.IsIPv4(addrpool_instance.Spec.Subnet) {
					// If the addresspool is from same network family
					// return it as associated network-addresspool object.
					// A network can have at most two network-addresspools,
					// one from each network family ie. IPv4 & IPv6.
					return &network_addrpool, addrpool
				}
			}
		}
	}

	return nil, nil
}

func (r *HostReconciler) GetAllNetworkAddressPoolData(
	client *gophercloud.ServiceClient,
	network_instance *starlingxv1.PlatformNetwork) (
	error,
	*networks.Network,
	[]networkAddressPools.NetworkAddressPool,
	[]addresspools.AddressPool) {

	system_network, err := GetSystemNetwork(client, network_instance)
	if err != nil {
		return err, nil, nil, nil
	}

	system_network_addrpools, err := networkAddressPools.ListNetworkAddressPools(client)
	if err != nil {
		logHost.Error(err, "failed to fetch network-addresspools from system")
		return err, nil, nil, nil
	}

	addrpool_list, err := addresspools.ListAddressPools(client)
	if err != nil {
		logHost.Error(err, "failed to fetch addresspools from system")
		return err, nil, nil, nil
	}

	return nil, system_network, system_network_addrpools, addrpool_list

}

func (r *HostReconciler) UpdateNetworkAddrPools(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork, addrpool_instance *starlingxv1.AddressPool) error {

	err, system_network, system_network_addrpools, addrpool_list := r.GetAllNetworkAddressPoolData(
		client, network_instance)
	if err != nil {
		return err
	}

	if system_network == nil {
		// No point in continuing if there is no network already
		return nil
	}

	system_addrpool, err := GetSystemAddrPool(client, addrpool_instance)
	if err != nil {
		return err
	} else if system_addrpool == nil {
		// No point in continuing if there is no addresspool already
		return nil
	}

	network_addrpool, associated_addrpool := GetAssociatedNetworkAddrPool(
		system_network,
		addrpool_instance,
		system_network_addrpools,
		addrpool_list)

	if network_addrpool != nil && associated_addrpool != nil {
		if associated_addrpool.ID != system_addrpool.ID {
			// Delete the associated network addrpool since it's not
			// linked to same address pool as the address pool spec.
			err := networkAddressPools.Delete(client, network_addrpool.UUID).ExtractErr()
			if err != nil {
				logHost.Error(err, "failed to delete associated network-addresspool")
				return err
			} else {
				log_msg := fmt.Sprintf(
					"Deleted network-addresspool object %s - %s",
					network_addrpool.NetworkName,
					network_addrpool.AddressPoolName)

				r.ReconcilerEventLogger.NormalEvent(network_instance, common.ResourceDeleted,
					log_msg)
			}
		} else {
			// No action required there is already network-addresspool
			// association created for given network and addresspool
			log_msg := fmt.Sprintf(
				"Found network-addresspool with %s - %s. No need to delete/recreate network-addresspool association.",
				network_addrpool.NetworkName,
				network_addrpool.AddressPoolName)

			logHost.V(2).Info(log_msg)

			return nil
		}
	}

	opts := networkAddressPools.NetworkAddressPoolOpts{}
	opts.NetworkUUID = &system_network.UUID
	opts.AddressPoolUUID = &system_addrpool.ID

	_, err = networkAddressPools.Create(client, opts).Extract()

	if err == nil {
		msg := fmt.Sprintf("Created new network-addrpool association %s - %s", system_network.Name, system_addrpool.Name)

		r.ReconcilerEventLogger.NormalEvent(network_instance, common.ResourceCreated,
			msg)
	} else {
		logHost.Error(err, "there was an error creating new network-addresspool.")
	}

	return err

}

func (r *HostReconciler) UpdateNetworkReconciliationStatus(
	network_instance *starlingxv1.PlatformNetwork,
	is_reconciled bool,
	should_reconcile bool) error {

	oldInSync := network_instance.Status.InSync

	if network_instance.Status.DeploymentScope == cloudManager.ScopeBootstrap {
		if !should_reconcile {
			// Prevents raising alarm if configuration of given network type
			// is unsupported in day-1 and system is out-of-sync.
			// Insync will serve as reconciliation indicator in this case.
			network_instance.Status.Reconciled = true
		} else {
			network_instance.Status.Reconciled = is_reconciled
		}
		network_instance.Status.InSync = is_reconciled
	}

	err := r.Client.Status().Update(context.TODO(), network_instance)
	if err != nil {
		logHost.Error(err, fmt.Sprintf("failed to update '%s' platform network status", network_instance.Name))
		return err
	}

	if oldInSync != network_instance.Status.InSync {
		r.ReconcilerEventLogger.NormalEvent(network_instance, common.ResourceUpdated,
			"%s network's synchronization has changed to: %t", network_instance.Name, network_instance.Status.InSync)
	}

	return nil
}

func (r *HostReconciler) UpdateAddrPoolReconciliationStatus(
	network_instance *starlingxv1.PlatformNetwork,
	addrpool_instance *starlingxv1.AddressPool,
	is_reconciled bool,
	should_reconcile bool) error {

	oldInSync := addrpool_instance.Status.InSync

	// AddressPool doesn't have deploymentScope by design.
	// It inherits deploymentScope of associated network.
	if network_instance.Status.DeploymentScope == cloudManager.ScopeBootstrap {
		if !should_reconcile {
			// Prevents raising alarm if configuration of given network type
			// is unsupported in day-1 and system is out-of-sync.
			// Insync will serve as reconciliation indicator in this case.
			addrpool_instance.Status.Reconciled = true
		} else {
			addrpool_instance.Status.Reconciled = is_reconciled
		}
		addrpool_instance.Status.InSync = is_reconciled
	}

	err := r.Client.Status().Update(context.TODO(), addrpool_instance)
	if err != nil {
		logHost.Error(err, fmt.Sprintf("failed to update '%s' addresspool status", addrpool_instance.Name))
		return err
	}

	if oldInSync != addrpool_instance.Status.InSync {
		r.ReconcilerEventLogger.NormalEvent(addrpool_instance, common.ResourceUpdated,
			"%s addresspool's synchronization has changed to: %t", addrpool_instance.Name, addrpool_instance.Status.InSync)
	}

	return nil
}

func (r *HostReconciler) ReconcileNetworkResource(client *gophercloud.ServiceClient, network_instance *starlingxv1.PlatformNetwork) (error, *bool) {

	system_network, err := GetSystemNetwork(client, network_instance)
	if err != nil {
		return err, nil
	}

	r.UpdateNetworkUUID(network_instance, system_network)

	opts, update_required, uuid := r.IsNetworkUpdateRequired(network_instance, system_network)

	err, should_reconcile := r.ShouldReconcile(client, network_instance, nil, update_required, uuid)
	if err != nil {
		return err, nil
	}

	if should_reconcile && update_required {
		err := r.CreateOrUpdateNetworks(client, opts, uuid, network_instance)
		if err == nil {
			// Make sure network UUID is synchronized
			system_network, err = GetSystemNetwork(client, network_instance)
			if err != nil {
				return err, nil
			}
			r.UpdateNetworkUUID(network_instance, system_network)
		}
		return err, &should_reconcile
	} else if update_required {
		err_msg := fmt.Sprintf(
			"There is delta between applied spec and system for platform network '%s'",
			network_instance.Name)
		err := perrors.New(err_msg)
		return err, &should_reconcile
	}

	return nil, &should_reconcile

}

func (r *HostReconciler) ReconcileNetworkAndAddressPools(
	client *gophercloud.ServiceClient,
	network_instance *starlingxv1.PlatformNetwork,
	addrpool_instance *starlingxv1.AddressPool,
	system_info *cloudManager.SystemInfo) error {

	err, should_reconcile := r.ReconcileNetworkResource(client, network_instance)
	if err != nil && should_reconcile == nil {
		// Some other error occured not related to reconciliation.
		// Eg. error listing networks by querying the system.
		// Request will be requeued.
		return err
	}

	is_reconciled := err == nil
	err_status := r.UpdateNetworkReconciliationStatus(
		network_instance,
		is_reconciled,
		*should_reconcile)

	if *should_reconcile && err != nil {
		//Reconciliation request will be requeued
		return err
	} else if err_status != nil {
		//Reconciliation request will be requeued
		return err_status
	}

	err, should_reconcile, validation_result := r.ReconcileAddrPoolResource(client, network_instance, addrpool_instance, system_info)
	if err != nil && should_reconcile == nil {
		// Some other error occured not related to reconciliation.
		// Eg. error listing networks by querying the system.
		// Request will be requeued.
		return err
	}

	is_reconciled = err == nil && *validation_result

	err_status = r.UpdateAddrPoolReconciliationStatus(
		network_instance,
		addrpool_instance,
		is_reconciled,
		*should_reconcile)

	// Update network-addresspool only if addresspool has been reconciled.
	if *should_reconcile && is_reconciled {
		logHost.V(2).Info(
			fmt.Sprintf("Updating network-addresspool association for network '%s' and addrpool '%s'",
				network_instance.Name, addrpool_instance.Name))
		update_err := r.UpdateNetworkAddrPools(client, network_instance, addrpool_instance)
		if update_err != nil {
			return update_err
		}
	}

	if *should_reconcile && err != nil {
		//Reconciliation request will be requeued
		return err
	} else if err_status != nil {
		//Reconciliation request will be requeued
		return err_status
	}

	return nil
}

func (r *HostReconciler) ReconcilePlatformNetworkBootstrap(
	client *gophercloud.ServiceClient,
	host_instance *starlingxv1.Host,
	network_instance *starlingxv1.PlatformNetwork,
	addrpool_instance *starlingxv1.AddressPool,
	system_info *cloudManager.SystemInfo) error {

	err := r.ReconcileNetworkAndAddressPools(client, network_instance, addrpool_instance, system_info)
	if err != nil {
		return err
	}

	return nil

}

func (r *HostReconciler) ReconcilePlatformNetworkAndAddrPoolResource(
	client *gophercloud.ServiceClient,
	host_instance *starlingxv1.Host,
	network_instance *starlingxv1.PlatformNetwork,
	addrpool_instance *starlingxv1.AddressPool,
	system_info *cloudManager.SystemInfo) error {

	if network_instance.Status.DeploymentScope == cloudManager.ScopeBootstrap {
		return r.ReconcilePlatformNetworkBootstrap(client, host_instance, network_instance, addrpool_instance, system_info)
	}
	return nil

}

func (r *HostReconciler) ReconcilePlatformNetworks(client *gophercloud.ServiceClient, instance *starlingxv1.Host, profile *starlingxv1.HostProfileSpec, host *v1info.HostInfo, system_info *cloudManager.SystemInfo) []error {
	var errs []error
	if !utils.IsReconcilerEnabled(utils.HostPlatformNetwork) {
		return nil
	}

	opts := kubeclient.ListOptions{}
	opts.Namespace = instance.Namespace
	platform_networks := &starlingxv1.PlatformNetworkList{}
	err := r.List(context.TODO(), platform_networks, &opts)
	if err != nil {
		err = perrors.Wrap(err, "failed to list platform networks")
		errs = append(errs, err)
	}

	for _, platform_network := range platform_networks.Items {
		platform_network_instance := &starlingxv1.PlatformNetwork{}
		platform_network_namespace := types.NamespacedName{Namespace: platform_network.ObjectMeta.Namespace, Name: platform_network.ObjectMeta.Name}
		err := r.Client.Get(context.TODO(), platform_network_namespace, platform_network_instance)
		if err != nil {
			logHost.Error(err, "Failed to get platform network resource from namespace")
			errs = append(errs, err)
		}

		for _, addrpool_name := range platform_network_instance.Spec.AssociatedAddressPools {
			addrpool_instance := &starlingxv1.AddressPool{}
			addrpool_namespace := types.NamespacedName{
				Namespace: platform_network.ObjectMeta.Namespace,
				Name:      addrpool_name}
			err := r.Client.Get(context.TODO(), addrpool_namespace, addrpool_instance)
			if err != nil {
				logHost.Error(err, "Failed to get addrpool resource from namespace")
				errs = append(errs, err)
			} else {
				err = r.ReconcilePlatformNetworkAndAddrPoolResource(client, instance, platform_network_instance, addrpool_instance, system_info)
				if err != nil {
					errs = append(errs, err)
				}
			}
		}
	}

	return errs
}