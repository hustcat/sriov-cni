package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/containernetworking/cni/pkg/ipam"
	"github.com/containernetworking/cni/pkg/ns"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/j-keck/arping"
	"github.com/vishvananda/netlink"

	. "github.com/hustcat/sriov-cni/config"
)

var locker *FileLocker

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

func setupPF(conf *SriovConf, ifName string, netns ns.NetNS) error {
	var (
		err error
	)

	masterName := conf.Net.Master
	args := conf.Args

	master, err := netlink.LinkByName(masterName)
	if err != nil {
		return fmt.Errorf("failed to lookup master %q: %v", masterName, err)
	}

	if args.MAC != "" {
		return fmt.Errorf("modifying mac address of PF is not supported")
	}

	if args.VLAN != 0 {
		return fmt.Errorf("modifying vlan of PF is not supported")
	}

	if err = netlink.LinkSetUp(master); err != nil {
		return fmt.Errorf("failed to setup PF")
	}

	// move PF device to ns
	if err = netlink.LinkSetNsFd(master, int(netns.Fd())); err != nil {
		return fmt.Errorf("failed to move PF to netns: %v", err)
	}

	return netns.Do(func(_ ns.NetNS) error {
		err := renameLink(masterName, ifName)
		if err != nil {
			return fmt.Errorf("failed to rename PF to %q: %v", ifName, err)
		}
		return nil
	})
}

func setupVF(conf *SriovConf, ifName string, netns ns.NetNS) error {
	var (
		err       error
		vfDevName string
	)

	vfIdx := 0
	masterName := conf.Net.Master
	args := conf.Args

	if args.VF != 0 {
		vfIdx = int(args.VF)
		vfDevName, err = getVFDeviceName(masterName, vfIdx)
		if err != nil {
			return err
		}
	} else {
		// alloc a free virtual function
		if vfIdx, vfDevName, err = allocFreeVF(masterName); err != nil {
			return err
		}
	}

	vfDev, err := netlink.LinkByName(vfDevName)
	if err != nil {
		return fmt.Errorf("failed to lookup vf device %q: %v", vfDevName, err)
	}

	if err = netlink.LinkSetUp(vfDev); err != nil {
		return fmt.Errorf("failed to setup vf %d device: %v", vfIdx, err)
	}

	// move VF device to ns
	if err = netlink.LinkSetNsFd(vfDev, int(netns.Fd())); err != nil {
		return fmt.Errorf("failed to move vf %d to netns: %v", vfIdx, err)
	}

	m, err := netlink.LinkByName(masterName)
	if err != nil {
		return fmt.Errorf("failed to lookup master %q: %v", masterName, err)
	}

	// set hardware address
	if args.MAC != "" {
		macAddr, err := net.ParseMAC(string(args.MAC))
		if err != nil {
			return err
		}
		if err = netlink.LinkSetVfHardwareAddr(m, vfIdx, macAddr); err != nil {
			return fmt.Errorf("failed to set vf %d macaddress: %v", vfIdx, err)
		}
	}

	if args.VLAN != 0 {
		if err = netlink.LinkSetVfVlan(m, vfIdx, int(args.VLAN)); err != nil {
			return fmt.Errorf("failed to set vf %d vlan: %v", vfIdx, err)
		}
	}

	return netns.Do(func(_ ns.NetNS) error {
		err := renameLink(vfDevName, ifName)
		if err != nil {
			return fmt.Errorf("failed to rename vf %d device %q to %q: %v", vfIdx, vfDevName, ifName, err)
		}
		return nil
	})
}

func releasePF(conf *SriovConf, ifName string, netns ns.NetNS) error {
	initns, err := ns.GetCurrentNS()
	if err != nil {
		return fmt.Errorf("failed to get init netns: %v", err)
	}

	// for IPAM in cmdDel
	return netns.Do(func(_ ns.NetNS) error {

		// get PF device
		// for kata containers, the network interface bind back to host by runtime
		// hence we'll skip this "failed to lookup device' error
		master, err := netlink.LinkByName(ifName)
		if err != nil {
			return nil
		}

		masterName := conf.Net.Master

		// shutdown PF device
		if err = netlink.LinkSetDown(master); err != nil {
			return fmt.Errorf("failed to down device: %v", err)
		}

		// rename PF device
		err = renameLink(ifName, masterName)
		if err != nil {
			return fmt.Errorf("failed to rename device %s to %s: %v", ifName, masterName, err)
		}

		// move PF device to init netns
		if err = netlink.LinkSetNsFd(master, int(initns.Fd())); err != nil {
			return fmt.Errorf("failed to move device %s to init netns: %v", ifName, err)
		}

		return nil
	})
}

func releaseVF(conf *SriovConf, ifName string, netns ns.NetNS) error {
	initns, err := ns.GetCurrentNS()
	if err != nil {
		return fmt.Errorf("failed to get init netns: %v", err)
	}

	// for IPAM in cmdDel
	return netns.Do(func(_ ns.NetNS) error {

		// get VF device
		// for kata containers, the network interface bind back to host by runtime
		// hence we'll skip this "failed to lookup device' error
		vfDev, err := netlink.LinkByName(ifName)
		if err != nil {
			return nil
		}

		// device name in init netns
		index := vfDev.Attrs().Index
		devName := fmt.Sprintf("dev%d", index)

		// shutdown VF device
		if err = netlink.LinkSetDown(vfDev); err != nil {
			return fmt.Errorf("failed to down device: %v", err)
		}

		// rename VF device
		err = renameLink(ifName, devName)
		if err != nil {
			return fmt.Errorf("failed to rename device %s to %s: %v", ifName, devName, err)
		}

		// move VF device to init netns
		if err = netlink.LinkSetNsFd(vfDev, int(initns.Fd())); err != nil {
			return fmt.Errorf("failed to move device %s to init netns: %v", ifName, err)
		}

		return nil
	})
}

func cmdAdd(args *skel.CmdArgs) error {
	n, err := LoadConf(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", args.Netns, err)
	}
	defer netns.Close()

	// use file lock to avoid race condition btw process
	locker.Lock()
	if n.Net.PFOnly != true {
		if err = setupVF(n, args.IfName, netns); err != nil {
			return err
		}
	} else {
		if err = setupPF(n, args.IfName, netns); err != nil {
			return err
		}
	}
	locker.Unlock()

	// run the IPAM plugin and get back the config to apply
	result, err := ipam.ExecAdd(n.Net.IPAM.Type, args.StdinData)
	if err != nil {
		return err
	}
	if result.IP4 == nil {
		return errors.New("IPAM plugin returned missing IPv4 config")
	}

	err = netns.Do(func(_ ns.NetNS) error {
		if err := ipam.ConfigureIface(args.IfName, result); err != nil {
			return err
		}
		// add arping, in case for arp ttl cache
		contEth, err := net.InterfaceByName(args.IfName)
		if err != nil {
			return err
		}
		if result.IP4 != nil {
			if err := arping.GratuitousArpOverIface(result.IP4.IP.IP, *contEth); err != nil {
				return err
			}
		}

		if result.IP6 != nil {
			if err := arping.GratuitousArpOverIface(result.IP6.IP.IP, *contEth); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	result.DNS = n.Net.DNS
	return result.Print()
}

func cmdDel(args *skel.CmdArgs) error {
	n, err := LoadConf(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		// according to:
		// https://github.com/kubernetes/kubernetes/issues/43014#issuecomment-287164444
		// if provided path does not exist (e.x. when node was restarted)
		// plugin should silently return with success after releasing
		// IPAM resources
		_, ok := err.(ns.NSPathNotExistErr)
		if ok {
			return nil
		}

		return fmt.Errorf("failed to open netns %q: %v", netns, err)
	}
	defer netns.Close()

	if n.Net.PFOnly != true {
		if err = releaseVF(n, args.IfName, netns); err != nil {
			return err
		}
	} else {
		if err = releasePF(n, args.IfName, netns); err != nil {
			return err
		}
	}

	err = ipam.ExecDel(n.Net.IPAM.Type, args.StdinData)
	if err != nil {
		return err
	}

	return nil
}

func renameLink(curName, newName string) error {
	link, err := netlink.LinkByName(curName)
	if err != nil {
		return err
	}

	return netlink.LinkSetName(link, newName)
}

func allocFreeVF(master string) (int, string, error) {
	vfIdx := -1
	devName := ""

	sriovFile := fmt.Sprintf("/sys/class/net/%s/device/sriov_numvfs", master)
	if _, err := os.Lstat(sriovFile); err != nil {
		return -1, "", fmt.Errorf("failed to open the sriov_numfs of device %q: %v", master, err)
	}

	data, err := ioutil.ReadFile(sriovFile)
	if err != nil {
		return -1, "", fmt.Errorf("failed to read the sriov_numfs of device %q: %v", master, err)
	}

	if len(data) == 0 {
		return -1, "", fmt.Errorf("no data in the file %q", sriovFile)
	}

	sriovNumfs := strings.TrimSpace(string(data))
	vfTotal, err := strconv.Atoi(sriovNumfs)
	if err != nil {
		return -1, "", fmt.Errorf("failed to convert sriov_numfs(byte value) to int of device %q: %v", master, err)
	}

	if vfTotal <= 0 {
		return -1, "", fmt.Errorf("no virtual function in the device %q", master)
	}

	for vf := 0; vf < vfTotal; vf++ {
		devName, err = getVFDeviceName(master, vf)

		// got a free vf
		if err == nil {
			vfIdx = vf
			break
		}
	}

	if vfIdx == -1 {
		return -1, "", fmt.Errorf("can not get a free virtual function in directory %s", master)
	}
	return vfIdx, devName, nil
}

func getVFDeviceName(master string, vf int) (string, error) {
	vfDir := fmt.Sprintf("/sys/class/net/%s/device/virtfn%d/net", master, vf)
	if _, err := os.Lstat(vfDir); err != nil {
		return "", fmt.Errorf("failed to open the virtfn%d dir of the device %q: %v", vf, master, err)
	}

	infos, err := ioutil.ReadDir(vfDir)
	if err != nil {
		return "", fmt.Errorf("failed to read the virtfn%d dir of the device %q: %v", vf, master, err)
	}

	if len(infos) != 1 {
		return "", fmt.Errorf("no network device in directory %s", vfDir)
	}
	return infos[0].Name(), nil
}

func main() {
	skel.PluginMain(cmdAdd, cmdDel, version.Legacy)
}
