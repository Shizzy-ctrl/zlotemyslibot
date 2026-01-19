package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/robfig/cron/v3"
)

type Config struct {
	GemChannelID   string   `json:"gem_channel_id"`
	GemSubscribers []string `json:"gem_subscribers"`
	TestChannelID  string   `json:"test_channel_id"`
	TestSubscriber string   `json:"test_subscriber"`
}

var (
	config     Config
	configFile = "config.json"
)

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("Brak tokena Discord! Ustaw zmienną DISCORD_TOKEN")
	}

	loadConfig()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("Błąd tworzenia sesji:", err)
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsGuildMessages

	// 🚀 CRON SCHEDULER zamiast tickera
	go startCronScheduler(dg)
	go startTestScheduler(dg)

	err = dg.Open()
	if err != nil {
		log.Fatal("Błąd otwierania połączenia:", err)
	}
	defer dg.Close()

	fmt.Println("Bot działa! Naciśnij CTRL+C aby zakończyć.")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func loadConfig() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		config = Config{
			GemChannelID:   "",
			GemSubscribers: nil,
			TestChannelID:  "",
			TestSubscriber: "",
		}
		saveConfig()
		return
	}
	json.Unmarshal(data, &config)
}

func saveConfig() {
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configFile, data, 0o644)
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	content := strings.TrimSpace(m.Content)

	if content == "!pomoc" {
		help := `**🤖 Bot - Komendy:**

!test - Uruchom testy repeaterów WiFi i pokaż podsumowanie
!gem - Wygeneruj wykres ETF jako PNG
!gemsubscribe - Zapisz się na miesięczny wykres ETF (ostatni dzień miesiąca, 10:00)
!pogoda - Pogoda na jutro
!pomoc - Pokaż tę pomoc`
		s.ChannelMessageSend(m.ChannelID, help)
	} else if content == "!test" {
		runTestsAndSendSummary(s, m.ChannelID)
	} else if content == "!gem" {
		statusMsg, statusErr := s.ChannelMessageSend(m.ChannelID, "⏳ Generuję wykres...")
		if err := generateAndSendGem(s, m.ChannelID); err != nil {
			log.Println("!gem error:", err)
			if statusErr == nil && statusMsg != nil {
				s.ChannelMessageDelete(m.ChannelID, statusMsg.ID)
			}
			s.ChannelMessageSend(m.ChannelID, "❌ Nie udało się wygenerować wykresu")
			return
		}
		if statusErr == nil && statusMsg != nil {
			s.ChannelMessageDelete(m.ChannelID, statusMsg.ID)
		}
	} else if content == "!gemsubscribe" {
		added := addGemSubscriber(m.Author.ID)
		config.GemChannelID = m.ChannelID
		saveConfig()
		if added {
			s.ChannelMessageSend(m.ChannelID, "✅ Zapisano na miesięczny wykres ETF. Ostatni dzień miesiąca o 10:00 wrzucę wykres i oznaczę zapisanych.")
		} else {
			s.ChannelMessageSend(m.ChannelID, "✅ Już jesteś zapisany. Ostatni dzień miesiąca o 10:00 wrzucę wykres i oznaczę zapisanych.")
		}
	} else if content == "!pogoda" {
		msg := buildTomorrowWeatherMessage()
		if msg == "" {
			s.ChannelMessageSend(m.ChannelID, "❌ Nie udało się pobrać prognozy")
			return
		}
		s.ChannelMessageSend(m.ChannelID, msg)
	}
}

func addGemSubscriber(userID string) bool {
	for _, id := range config.GemSubscribers {
		if id == userID {
			return false
		}
	}
	config.GemSubscribers = append(config.GemSubscribers, userID)
	return true
}

func isLastDayOfMonth(t time.Time) bool {
	nextDay := t.AddDate(0, 0, 1)
	return nextDay.Month() != t.Month()
}

func mentionGemSubscribers() string {
	if len(config.GemSubscribers) == 0 {
		return ""
	}
	var b strings.Builder
	for i, id := range config.GemSubscribers {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString("<@")
		b.WriteString(id)
		b.WriteString(">")
	}
	return b.String()
}

func generateAndSendGem(s *discordgo.Session, channelID string) error {
	tmpDir := os.TempDir()
	outputPath := filepath.Join(tmpDir, fmt.Sprintf("gem_%d.png", time.Now().UnixNano()))

	if err := generateGemChart(outputPath); err != nil {
		return err
	}

	defer os.Remove(outputPath)

	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = s.ChannelFileSend(channelID, "etfs_rok.png", file)
	return err
}

func startCronScheduler(s *discordgo.Session) {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		log.Fatal("Location error:", err)
	}

	c := cron.New(cron.WithLocation(loc))

	_, err = c.AddFunc("0 10 * * *", func() {
		now := time.Now().In(loc)
		if !isLastDayOfMonth(now) {
			return
		}
		if config.GemChannelID == "" || len(config.GemSubscribers) == 0 {
			return
		}
		if msg := mentionGemSubscribers(); msg != "" {
			s.ChannelMessageSend(config.GemChannelID, msg)
		}
		if err := generateAndSendGem(s, config.GemChannelID); err != nil {
			log.Println("scheduled gem error:", err)
			s.ChannelMessageSend(config.GemChannelID, "❌ Nie udało się wygenerować wykresu")
		}
	})
	if err != nil {
		log.Fatal("Cron AddFunc błąd:", err)
	}

	_, err = c.AddFunc("0 19 * * *", func() {
		if config.GemChannelID == "" || len(config.GemSubscribers) == 0 {
			return
		}
		msg := buildTomorrowWeatherMessage()
		if msg == "" {
			return
		}
		mention := mentionGemSubscribers()
		if mention != "" {
			msg = mention + "\n" + msg
		}
		s.ChannelMessageSend(config.GemChannelID, msg)
	})
	if err != nil {
		log.Fatal("Cron AddFunc błąd:", err)
	}

	fmt.Println("✅ Cron działa!")
	c.Start()
}

type weatherResponse struct {
	Daily struct {
		Time           []string  `json:"time"`
		TemperatureMax []float64 `json:"temperature_2m_max"`
		TemperatureMin []float64 `json:"temperature_2m_min"`
		WeatherCode    []int     `json:"weathercode"`
	} `json:"daily"`
}

type forecast struct {
	MinC float64
	MaxC float64
	Code int
	Date string
}

func buildTomorrowWeatherMessage() string {
	lesna, err := fetchTomorrowForecast(51.0156, 15.2634)
	if err != nil {
		log.Println("weather Lesna error:", err)
		return ""
	}
	bielsko, err := fetchTomorrowForecast(49.8224, 19.0469)
	if err != nil {
		log.Println("weather Bielsko error:", err)
		return ""
	}

	var b strings.Builder
	b.WriteString("🌤️ **Pogoda na jutro**\n")
	b.WriteString(fmt.Sprintf("Leśna: %s, %.0f/%.0f°C\n", weatherDescription(lesna.Code), lesna.MinC, lesna.MaxC))
	b.WriteString(fmt.Sprintf("Bielsko-Biała: %s, %.0f/%.0f°C", weatherDescription(bielsko.Code), bielsko.MinC, bielsko.MaxC))
	return b.String()
}

func fetchTomorrowForecast(lat, lon float64) (forecast, error) {
	url := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&daily=temperature_2m_max,temperature_2m_min,weathercode&timezone=Europe/Warsaw&forecast_days=2", lat, lon)
	resp, err := http.Get(url)
	if err != nil {
		return forecast{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return forecast{}, fmt.Errorf("bad status: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return forecast{}, err
	}
	var parsed weatherResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return forecast{}, err
	}
	if len(parsed.Daily.Time) < 2 || len(parsed.Daily.TemperatureMax) < 2 || len(parsed.Daily.TemperatureMin) < 2 || len(parsed.Daily.WeatherCode) < 2 {
		return forecast{}, fmt.Errorf("insufficient forecast data")
	}

	return forecast{
		Date: parsed.Daily.Time[1],
		MaxC: parsed.Daily.TemperatureMax[1],
		MinC: parsed.Daily.TemperatureMin[1],
		Code: parsed.Daily.WeatherCode[1],
	}, nil
}

func weatherDescription(code int) string {
	switch code {
	case 0:
		return "bezchmurnie"
	case 1, 2, 3:
		return "częściowe zachmurzenie"
	case 45, 48:
		return "mgła"
	case 51, 53, 55:
		return "mżawka"
	case 56, 57:
		return "marznąca mżawka"
	case 61, 63, 65:
		return "deszcz"
	case 66, 67:
		return "marznący deszcz"
	case 71, 73, 75:
		return "śnieg"
	case 77:
		return "ziarna śniegu"
	case 80, 81, 82:
		return "przelotne opady"
	case 85, 86:
		return "przelotne opady śniegu"
	case 95:
		return "burza"
	case 96, 99:
		return "burza z gradem"
	default:
		return "pogoda"
	}
}
