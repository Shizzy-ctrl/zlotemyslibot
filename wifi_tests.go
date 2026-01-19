package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type PingStats struct {
	PacketsSent     int
	PacketsReceived int
	PacketLoss      float64
	MinRTT          float64
	AvgRTT          float64
	MaxRTT          float64
	StdDevRTT       float64
}

type WiFiNetwork struct {
	SSID  string
	Index int
}

type NetworkResult struct {
	Success bool
	LocalIP string
}

type TestResult struct {
	SSID       string
	Success    bool
	LocalIP    string
	PacketLoss float64
	MinRTT     float64
	AvgRTT     float64
	MaxRTT     float64
	StdDevRTT  float64
	Error      string
	Target     string
	Count      int
}

var testMu sync.Mutex

func startTestScheduler(s *discordgo.Session) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		runTestsAndNotifyOnFailure(s)
	}
}

func runTestsAndSendSummary(s *discordgo.Session, channelID string) {
	testMu.Lock()
	defer testMu.Unlock()

	results, err := runAllTests()
	if err != nil {
		s.ChannelMessageSend(channelID, fmt.Sprintf("❌ Błąd testów: %v", err))
		return
	}
	if len(results) == 0 {
		s.ChannelMessageSend(channelID, "⚠️ Brak sieci do przetestowania")
		return
	}
	s.ChannelMessageSend(channelID, buildTestSummary(results))
}

func runTestsAndNotifyOnFailure(s *discordgo.Session) {
	if config.TestChannelID == "" {
		return
	}

	testMu.Lock()
	defer testMu.Unlock()

	results, err := runAllTests()
	if err != nil {
		s.ChannelMessageSend(config.TestChannelID, fmt.Sprintf("❌ Błąd testów: %v", err))
		return
	}
	if len(results) == 0 {
		return
	}
	if msg := buildFailureMessage(results); msg != "" {
		s.ChannelMessageSend(config.TestChannelID, msg)
	}
}

func buildTestSummary(results []TestResult) string {
	var b strings.Builder
	b.WriteString("📋 **PODSUMOWANIE TESTÓW**\n\n")

	good := make([]TestResult, 0, len(results))
	bad := make([]TestResult, 0, len(results))
	for _, r := range results {
		if r.Success {
			good = append(good, r)
		} else {
			bad = append(bad, r)
		}
	}

	if len(good) > 0 {
		b.WriteString(fmt.Sprintf("✅ Sieci OK (%d):\n", len(good)))
		for _, r := range good {
			ip := r.LocalIP
			if ip == "" {
				ip = "brak IP"
			}
			b.WriteString(fmt.Sprintf("• %s (IP: %s) loss=%.1f%% avg=%.1fms\n", r.SSID, ip, r.PacketLoss, r.AvgRTT))
		}
		b.WriteString("\n")
	}

	if len(bad) > 0 {
		b.WriteString(fmt.Sprintf("❌ Sieci z problemami (%d):\n", len(bad)))
		for _, r := range bad {
			ip := r.LocalIP
			if ip == "" {
				ip = "brak IP"
			}
			b.WriteString(fmt.Sprintf("• %s (IP: %s) loss=%.1f%% avg=%.1fms min=%.1fms max=%.1fms\n", r.SSID, ip, r.PacketLoss, r.AvgRTT, r.MinRTT, r.MaxRTT))
		}
	}

	return strings.TrimSpace(b.String())
}

func buildFailureMessage(results []TestResult) string {
	failed := make([]TestResult, 0, len(results))
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		return ""
	}

	var b strings.Builder
	if config.TestSubscriber != "" {
		b.WriteString("<@")
		b.WriteString(config.TestSubscriber)
		b.WriteString("> ")
	}
	b.WriteString("❌ Testy nieudane:\n")
	for _, r := range failed {
		ipInfo := "brak IP"
		if r.LocalIP != "" {
			ipInfo = r.LocalIP
		}
		b.WriteString(fmt.Sprintf("• %s (IP: %s) target=%s count=%d loss=%.1f%% rtt avg=%.1fms min=%.1fms max=%.1fms stddev=%.1fms\n",
			r.SSID, ipInfo, r.Target, r.Count, r.PacketLoss, r.AvgRTT, r.MinRTT, r.MaxRTT, r.StdDevRTT))
	}

	return strings.TrimSpace(b.String())
}

func getWiFiNetworksFromDB() ([]WiFiNetwork, error) {
	dbPath := "/var/lib/dietpi/dietpi-config/.wifi_network_db"

	file, err := os.Open(dbPath)
	if err != nil {
		dbPath = "/boot/dietpi/.network"
		file, err = os.Open(dbPath)
		if err != nil {
			return getNetworksFromWpaSupplicant()
		}
	}
	defer file.Close()

	var networks []WiFiNetwork
	scanner := bufio.NewScanner(file)
	index := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) > 0 && parts[0] != "" {
			networks = append(networks, WiFiNetwork{
				SSID:  parts[0],
				Index: index,
			})
			index++
		}
	}

	if len(networks) == 0 {
		return getNetworksFromWpaSupplicant()
	}

	return networks, nil
}

func getNetworksFromWpaSupplicant() ([]WiFiNetwork, error) {
	wpaPath := "/etc/wpa_supplicant/wpa_supplicant.conf"

	file, err := os.Open(wpaPath)
	if err != nil {
		return nil, fmt.Errorf("nie można odczytać zapisanych sieci WiFi")
	}
	defer file.Close()

	var networks []WiFiNetwork
	scanner := bufio.NewScanner(file)
	var currentSSID string
	index := 0

	ssidRegex := regexp.MustCompile(`ssid="([^"]+)"`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if match := ssidRegex.FindStringSubmatch(line); len(match) > 1 {
			currentSSID = match[1]
		}

		if strings.HasPrefix(line, "}") && currentSSID != "" {
			networks = append(networks, WiFiNetwork{
				SSID:  currentSSID,
				Index: index,
			})
			currentSSID = ""
			index++
		}
	}

	return networks, nil
}

func testNetwork(network WiFiNetwork) (bool, string, *PingStats) {
	logf("📡 Sieć: %s\n\n", network.SSID)

	currentSSID := getCurrentSSID()

	if currentSSID == network.SSID {
		logf("✅ Już połączony z tą siecią!\n\n")
	} else {
		logf("🔌 Łączenie z siecią...\n")
		connected := connectToNetwork(network.SSID)

		if !connected {
			logf("❌ Nie udało się połączyć z siecią\n")
			logf("💡 Sprawdź czy hasło jest poprawne w konfiguracji\n")
			return false, "", nil
		}

		logf("✅ Połączono pomyślnie!\n\n")
	}

	showConnectionInfo()

	time.Sleep(3 * time.Second)

	allTestsPassed := true

	logf("--- Test połączenia ---\n")

	logf("\n🌐 Test: 8.8.8.8 (Google DNS)\n")
	stats, err := runPingTest("8.8.8.8", 20)
	if err != nil {
		logf("❌ Błąd: %v\n", err)
		allTestsPassed = false
	} else {
		displayResults(stats)
		if stats.PacketLoss > 5 {
			allTestsPassed = false
		}
	}

	logf("\n📋 Podsumowanie dla %s:\n", network.SSID)

	if allTestsPassed {
		logf("  ✅ Sieć działa prawidłowo\n")
	} else {
		logf("  ❌ Sieć ma problemy z packet loss lub opóźnieniami\n")
	}

	return allTestsPassed, getLocalIP(), stats
}

func getCurrentSSID() string {
	cmd := exec.Command("iwgetid", "-r")
	output, err := cmd.Output()

	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

func connectToNetwork(ssid string) bool {
	currentSSID := getCurrentSSID()
	if currentSSID != "" && currentSSID != ssid {
		logf("🔄 Rozłączanie z %s...\n", currentSSID)
		cmd := exec.Command("wpa_cli", "-i", "wlan0", "disconnect")
		cmd.Run()
		time.Sleep(2 * time.Second)
	}

	cmd := exec.Command("wpa_cli", "-i", "wlan0", "list_networks")
	output, err := cmd.Output()

	if err != nil {
		logf("⚠️  Błąd wpa_cli: %v\n", err)
		return false
	}

	lines := strings.Split(string(output), "\n")
	networkID := -1

	for _, line := range lines {
		if strings.Contains(line, ssid) {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				networkID, _ = strconv.Atoi(parts[0])
				break
			}
		}
	}

	if networkID == -1 {
		logf("⚠️  Nie znaleziono sieci '%s' w wpa_supplicant\n", ssid)
		logf("💡 Dodaj sieć przez: dietpi-config -> Network Options -> WiFi\n")
		return false
	}

	logf("🔌 Aktywuję połączenie (network ID: %d)...\n", networkID)
	cmd = exec.Command("wpa_cli", "-i", "wlan0", "select_network", strconv.Itoa(networkID))
	output, err = cmd.CombinedOutput()

	if err != nil {
		logf("⚠️  Błąd: %s\n", strings.TrimSpace(string(output)))
		return false
	}

	logf("⏳ Czekam na połączenie...\n")
	time.Sleep(8 * time.Second)

	if checkWiFiConnection(ssid) {
		cmd = exec.Command("dhclient", "wlan0")
		cmd.Run()
		time.Sleep(2 * time.Second)
		return true
	}

	return false
}

func checkWiFiConnection(expectedSSID string) bool {
	cmd := exec.Command("iwgetid", "-r")
	output, err := cmd.Output()

	if err != nil {
		return false
	}

	currentSSID := strings.TrimSpace(string(output))
	return currentSSID == expectedSSID
}

func showConnectionInfo() {
	cmd := exec.Command("iwconfig", "wlan0")
	output, _ := cmd.Output()

	info := string(output)

	signalRegex := regexp.MustCompile(`Signal level=(-?\d+) dBm`)
	if match := signalRegex.FindStringSubmatch(info); len(match) > 1 {
		signal, _ := strconv.Atoi(match[1])
		logf("📶 Siła sygnału: %d dBm ", signal)

		if signal > -50 {
			logf("(✅ Doskonały)\n")
		} else if signal > -60 {
			logf("(✅ Bardzo dobry)\n")
		} else if signal > -70 {
			logf("(⚠️  Dobry)\n")
		} else {
			logf("(❌ Słaby)\n")
		}
	}

	bitrateRegex := regexp.MustCompile(`Bit Rate[=:]([\d.]+\s*\w+/s)`)
	if match := bitrateRegex.FindStringSubmatch(info); len(match) > 1 {
		logf("⚡ Prędkość: %s\n", match[1])
	}

	logln()
}

func runPingTest(target string, count int) (*PingStats, error) {
	cmd := exec.Command("ping", "-c", strconv.Itoa(count), "-W", "2", target)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return nil, fmt.Errorf("ping nie powiódł się")
	}

	return parsePingOutput(string(output))
}

func parsePingOutput(output string) (*PingStats, error) {
	stats := &PingStats{}

	packetRegex := regexp.MustCompile(`(\d+) packets transmitted, (\d+) received, ([\d.]+)% packet loss`)
	matches := packetRegex.FindStringSubmatch(output)

	if len(matches) > 0 {
		stats.PacketsSent, _ = strconv.Atoi(matches[1])
		stats.PacketsReceived, _ = strconv.Atoi(matches[2])
		stats.PacketLoss, _ = strconv.ParseFloat(matches[3], 64)
	}

	rttRegex := regexp.MustCompile(`rtt min/avg/max/(?:mdev|stddev) = ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)`)
	rttMatches := rttRegex.FindStringSubmatch(output)

	if len(rttMatches) > 0 {
		stats.MinRTT, _ = strconv.ParseFloat(rttMatches[1], 64)
		stats.AvgRTT, _ = strconv.ParseFloat(rttMatches[2], 64)
		stats.MaxRTT, _ = strconv.ParseFloat(rttMatches[3], 64)
		stats.StdDevRTT, _ = strconv.ParseFloat(rttMatches[4], 64)
	}

	return stats, nil
}

func displayResults(stats *PingStats) {
	logf("  Pakiety: %d wysłane, %d odebrane\n", stats.PacketsSent, stats.PacketsReceived)

	var lossIndicator string
	if stats.PacketLoss == 0 {
		lossIndicator = "✅"
	} else if stats.PacketLoss < 5 {
		lossIndicator = "⚠️"
	} else {
		lossIndicator = "❌"
	}

	logf("  Utrata: %.1f%% %s\n", stats.PacketLoss, lossIndicator)

	if stats.AvgRTT > 0 {
		logf("  RTT: min=%.1fms avg=%.1fms max=%.1fms stddev=%.1fms ",
			stats.MinRTT, stats.AvgRTT, stats.MaxRTT, stats.StdDevRTT)

		if stats.StdDevRTT < 10 {
			logf("✅\n")
		} else if stats.StdDevRTT < 30 {
			logf("⚠️\n")
		} else {
			logf("❌\n")
		}
	}
	logln()
}

func getLocalIP() string {
	cmd := exec.Command("ip", "-4", "addr", "show", "wlan0")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	ipRegex := regexp.MustCompile(`inet (\d+\.\d+\.\d+\.\d+)`)
	if match := ipRegex.FindStringSubmatch(string(output)); len(match) > 1 {
		return match[1]
	}
	return ""
}

func runAllTests() ([]TestResult, error) {
	logf("=== Tester Sieci WiFi ===\n")
	logf("Data: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	networks, err := getWiFiNetworksFromDB()
	if err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, nil
	}

	results := make([]TestResult, 0, len(networks))
	for i, network := range networks {
		logf("\n" + strings.Repeat("=", 60) + "\n")
		logf("Test sieci %d/%d: %s\n", i+1, len(networks), network.SSID)
		logf(strings.Repeat("=", 60) + "\n\n")

		success, localIP, stats := testNetwork(network)
		result := TestResult{
			SSID:    network.SSID,
			Success: success,
			LocalIP: localIP,
			Target:  "8.8.8.8",
			Count:   20,
		}
		if stats != nil {
			result.PacketLoss = stats.PacketLoss
			result.MinRTT = stats.MinRTT
			result.AvgRTT = stats.AvgRTT
			result.MaxRTT = stats.MaxRTT
			result.StdDevRTT = stats.StdDevRTT
		}
		if i < len(networks)-1 {
			logf("\nCzekam 5 sekund przed kolejną siecią...\n")
			time.Sleep(5 * time.Second)
		}
		results = append(results, result)
	}

	return results, nil
}

func logf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

func logln(args ...interface{}) {
	fmt.Println(args...)
}
