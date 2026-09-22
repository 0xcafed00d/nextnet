package esp

import (
	"fmt"
	"net"
)

// StationInfo is the Linux network identity presented through ESP-AT CIFSR.
// It describes an interface only; nextnet never changes Linux network setup.
type StationInfo struct {
	IPAddress  string
	MACAddress string
}

func discoverStationInfo() (StationInfo, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return StationInfo{}, fmt.Errorf("list network interfaces: %w", err)
	}

	var fallback *StationInfo
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			ipv4 := ip.To4()
			if ipv4 == nil || ipv4.IsLoopback() || ipv4.IsUnspecified() {
				continue
			}
			macAddress := networkInterface.HardwareAddr.String()
			if macAddress == "" {
				macAddress = "00:00:00:00:00:00"
			}
			info := StationInfo{IPAddress: ipv4.String(), MACAddress: macAddress}
			if !ipv4.IsLinkLocalUnicast() {
				return info, nil
			}
			if fallback == nil {
				fallback = &info
			}
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return StationInfo{}, fmt.Errorf("no active non-loopback IPv4 interface")
}
