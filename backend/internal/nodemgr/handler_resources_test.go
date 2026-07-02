package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
)

// --- Helper: build a sub-frame (no message type byte) ---
func buildSubFrame(fields func(enc *frame.Encoder)) []byte {
	enc := frame.SubEncoder()
	fields(enc)
	return enc.Bytes()
}

// --- decodeUARTEntry tests ---

func TestDecodeUARTEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  uartEntry
	}{
		{
			name: "full UART entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "uart0")
				enc.EncodeVarint(2, 1)   // port
				enc.EncodeVarint(3, 43)  // tx_pin
				enc.EncodeVarint(4, 44)  // rx_pin
				enc.EncodeVarint(5, 921600) // max_baud
				enc.EncodeVarint(6, 1)   // flags (bit0=DMA)
			},
			want: uartEntry{
				ID: "uart0", Port: 1, DefaultTxPin: 43, DefaultRxPin: 44,
				MaxBaud: 921600, Flags: 1, DmaSupported: true,
			},
		},
		{
			name: "UART no DMA flag",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "uart1")
				enc.EncodeVarint(2, 2)
				enc.EncodeVarint(5, 115200)
				enc.EncodeVarint(6, 0)
			},
			want: uartEntry{
				ID: "uart1", Port: 2, MaxBaud: 115200, Flags: 0, DmaSupported: false,
			},
		},
		{
			name: "UART minimal - only ID",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "u2")
			},
			want: uartEntry{ID: "u2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeUARTEntry(data)
			if got != tt.want {
				t.Errorf("decodeUARTEntry() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeUARTEntry_Empty(t *testing.T) {
	// Empty data should return zero-value uartEntry (NewSubDecoder returns error)
	got := decodeUARTEntry([]byte{})
	if got.ID != "" || got.Port != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeI2CEntry tests ---

func TestDecodeI2CEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  i2cEntry
	}{
		{
			name: "full I2C entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "i2c0")
				enc.EncodeVarint(2, 0)  // port
				enc.EncodeVarint(3, 21) // sda
				enc.EncodeVarint(4, 22) // scl
				enc.EncodeVarint(5, 400000) // max_freq
				enc.EncodeVarint(6, 1)  // flags (DMA)
			},
			want: i2cEntry{
				ID: "i2c0", Port: 0, DefaultSdaPin: 21, DefaultSclPin: 22,
				MaxFreqHz: 400000, Flags: 1, DmaSupported: true,
			},
		},
		{
			name: "I2C no DMA",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "i2c1")
				enc.EncodeVarint(2, 1)
				enc.EncodeVarint(6, 2) // flags bit0=0, so no DMA
			},
			want: i2cEntry{
				ID: "i2c1", Port: 1, Flags: 2, DmaSupported: false,
			},
		},
		{
			name: "I2C minimal",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "i2c_min")
			},
			want: i2cEntry{ID: "i2c_min"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeI2CEntry(data)
			if got != tt.want {
				t.Errorf("decodeI2CEntry() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeI2CEntry_Empty(t *testing.T) {
	got := decodeI2CEntry([]byte{})
	if got.ID != "" || got.Port != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeSPIEntry tests ---

func TestDecodeSPIEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  spiEntry
	}{
		{
			name: "full SPI entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "spi0")
				enc.EncodeVarint(2, 2)  // port
				enc.EncodeVarint(3, 11) // mosi
				enc.EncodeVarint(4, 12) // miso
				enc.EncodeVarint(5, 13) // sclk
				enc.EncodeVarint(6, 10) // cs
				enc.EncodeVarint(7, 80000000) // max_freq
				enc.EncodeVarint(8, 1)  // flags (DMA)
			},
			want: spiEntry{
				ID: "spi0", Port: 2, DefaultMosiPin: 11, DefaultMisoPin: 12,
				DefaultSclkPin: 13, DefaultCsPin: 10, MaxFreqHz: 80000000,
				Flags: 1, DmaSupported: true,
			},
		},
		{
			name: "SPI no DMA",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "spi1")
				enc.EncodeVarint(2, 3)
				enc.EncodeVarint(8, 0)
			},
			want: spiEntry{
				ID: "spi1", Port: 3, Flags: 0, DmaSupported: false,
			},
		},
		{
			name: "SPI minimal",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "spi_min")
			},
			want: spiEntry{ID: "spi_min"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeSPIEntry(data)
			if got != tt.want {
				t.Errorf("decodeSPIEntry() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeSPIEntry_Empty(t *testing.T) {
	got := decodeSPIEntry([]byte{})
	if got.ID != "" || got.Port != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeGPIOEntry tests ---

func TestDecodeGPIOEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  gpioEntry
	}{
		{
			name: "GPIO entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "gpio5")
				enc.EncodeVarint(2, 5)
			},
			want: gpioEntry{ID: "gpio5", Pin: 5},
		},
		{
			name: "GPIO pin 0",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "gpio0")
				enc.EncodeVarint(2, 0)
			},
			want: gpioEntry{ID: "gpio0", Pin: 0},
		},
		{
			name: "GPIO minimal",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "g1")
			},
			want: gpioEntry{ID: "g1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeGPIOEntry(data)
			if got != tt.want {
				t.Errorf("decodeGPIOEntry() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeGPIOEntry_Empty(t *testing.T) {
	got := decodeGPIOEntry([]byte{})
	if got.ID != "" || got.Pin != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeADCEntry tests ---

func TestDecodeADCEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  adcEntry
	}{
		{
			name: "full ADC entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "adc1_ch0")
				enc.EncodeVarint(2, 1)  // unit
				enc.EncodeVarint(3, 0)  // channel
				enc.EncodeVarint(4, 32) // pin
				enc.EncodeVarint(5, 12) // max_bits
			},
			want: adcEntry{ID: "adc1_ch0", Unit: 1, Channel: 0, Pin: 32, MaxBits: 12},
		},
		{
			name: "ADC minimal",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "adc_min")
			},
			want: adcEntry{ID: "adc_min"},
		},
		{
			name: "ADC 16-bit",
			build: func(enc *frame.Encoder) {
				enc.EncodeString(1, "adc16")
				enc.EncodeVarint(2, 2)
				enc.EncodeVarint(3, 3)
				enc.EncodeVarint(4, 4)
				enc.EncodeVarint(5, 16)
			},
			want: adcEntry{ID: "adc16", Unit: 2, Channel: 3, Pin: 4, MaxBits: 16},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeADCEntry(data)
			if got != tt.want {
				t.Errorf("decodeADCEntry() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeADCEntry_Empty(t *testing.T) {
	got := decodeADCEntry([]byte{})
	if got.ID != "" || got.Unit != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- busTypeToString table-driven tests ---

func TestBusTypeToString(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{1, "UART"},
		{2, "I2C"},
		{3, "SPI"},
		{4, "GPIO"},
		{5, "ADC"},
		{0, "UNKNOWN_0"},
		{6, "UNKNOWN_6"},
		{100, "UNKNOWN_100"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := busTypeToString(tt.input)
			if got != tt.want {
				t.Errorf("busTypeToString(%d) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

// --- decodeBusesBlob multi-bus combo test ---

func TestDecodeBusesBlob(t *testing.T) {
	// Build a buses blob with one UART, one I2C, one GPIO, one ADC
	uartData := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "uart0")
		enc.EncodeVarint(2, 1)
		enc.EncodeVarint(6, 1) // flags with DMA
	})
	i2cData := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "i2c0")
		enc.EncodeVarint(2, 0)
		enc.EncodeVarint(5, 400000)
	})
	gpioData := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "gpio4")
		enc.EncodeVarint(2, 4)
	})
	adcData := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "adc0")
		enc.EncodeVarint(5, 12)
	})

	busesBlob := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeBytes(1, uartData)  // field 1 = UART
		enc.EncodeBytes(2, i2cData)   // field 2 = I2C
		enc.EncodeBytes(4, gpioData)  // field 4 = GPIO
		enc.EncodeBytes(5, adcData)   // field 5 = ADC
	})

	buses := decodeBusesBlob(busesBlob)

	if len(buses.UART) != 1 {
		t.Fatalf("UART count: got %d, want 1", len(buses.UART))
	}
	if buses.UART[0].ID != "uart0" || buses.UART[0].Port != 1 || !buses.UART[0].DmaSupported {
		t.Errorf("UART entry: %+v", buses.UART[0])
	}

	if len(buses.I2C) != 1 {
		t.Fatalf("I2C count: got %d, want 1", len(buses.I2C))
	}
	if buses.I2C[0].ID != "i2c0" || buses.I2C[0].MaxFreqHz != 400000 {
		t.Errorf("I2C entry: %+v", buses.I2C[0])
	}

	if len(buses.GPIO) != 1 {
		t.Fatalf("GPIO count: got %d, want 1", len(buses.GPIO))
	}
	if buses.GPIO[0].ID != "gpio4" || buses.GPIO[0].Pin != 4 {
		t.Errorf("GPIO entry: %+v", buses.GPIO[0])
	}

	if len(buses.ADC) != 1 {
		t.Fatalf("ADC count: got %d, want 1", len(buses.ADC))
	}
	if buses.ADC[0].ID != "adc0" || buses.ADC[0].MaxBits != 12 {
		t.Errorf("ADC entry: %+v", buses.ADC[0])
	}

	// SPI should be empty
	if len(buses.SPI) != 0 {
		t.Errorf("SPI count: got %d, want 0", len(buses.SPI))
	}
}

func TestDecodeBusesBlob_MultipleSameType(t *testing.T) {
	uart1 := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "uart0")
		enc.EncodeVarint(2, 1)
	})
	uart2 := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeString(1, "uart1")
		enc.EncodeVarint(2, 2)
	})

	busesBlob := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeBytes(1, uart1)
		enc.EncodeBytes(1, uart2)
	})

	buses := decodeBusesBlob(busesBlob)
	if len(buses.UART) != 2 {
		t.Fatalf("UART count: got %d, want 2", len(buses.UART))
	}
	if buses.UART[0].ID != "uart0" || buses.UART[1].ID != "uart1" {
		t.Errorf("UART IDs: %s, %s", buses.UART[0].ID, buses.UART[1].ID)
	}
}

func TestDecodeBusesBlob_Empty(t *testing.T) {
	buses := decodeBusesBlob([]byte{})
	if len(buses.UART) != 0 || len(buses.I2C) != 0 || len(buses.SPI) != 0 ||
		len(buses.GPIO) != 0 || len(buses.ADC) != 0 {
		t.Errorf("expected all empty for empty blob, got %+v", buses)
	}
}

// --- decodeChannelEntry table-driven tests ---

func TestDecodeChannelEntry(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  channelEntry
	}{
		{
			name: "full channel entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 1)     // id
				enc.EncodeVarint(2, 2)     // bus_type = I2C
				enc.EncodeVarint(3, 0x76)  // hardware_id
				enc.EncodeVarint(4, 5000)  // interval_ms
				enc.EncodeBool(5, true)    // enabled
				enc.EncodeBytes(6, []byte{0x01, 0x02}) // bus_config
				enc.EncodeVarint(7, 1)     // template_id
				enc.EncodeVarint(7, 2)     // template_id (repeated)
				enc.EncodeBool(8, true)    // dma_enabled
			},
			want: channelEntry{
				ID: 1, BusType: 2, HardwareID: 0x76, IntervalMs: 5000,
				Enabled: true, BusConfig: []byte{0x01, 0x02},
				TemplateIDs: []uint64{1, 2}, DmaEnabled: true,
			},
		},
		{
			name: "minimal channel entry",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 5)
				enc.EncodeVarint(2, 1) // UART
			},
			want: channelEntry{
				ID: 5, BusType: 1, TemplateIDs: nil,
			},
		},
		{
			name: "channel with DMA disabled",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 10)
				enc.EncodeBool(5, false)
				enc.EncodeBool(8, false)
			},
			want: channelEntry{
				ID: 10, Enabled: false, DmaEnabled: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeChannelEntry(data)
			// Compare field by field (BusConfig is []byte, TemplateIDs is []uint64)
			if got.ID != tt.want.ID {
				t.Errorf("ID: got %d, want %d", got.ID, tt.want.ID)
			}
			if got.BusType != tt.want.BusType {
				t.Errorf("BusType: got %d, want %d", got.BusType, tt.want.BusType)
			}
			if got.HardwareID != tt.want.HardwareID {
				t.Errorf("HardwareID: got %d, want %d", got.HardwareID, tt.want.HardwareID)
			}
			if got.IntervalMs != tt.want.IntervalMs {
				t.Errorf("IntervalMs: got %d, want %d", got.IntervalMs, tt.want.IntervalMs)
			}
			if got.Enabled != tt.want.Enabled {
				t.Errorf("Enabled: got %v, want %v", got.Enabled, tt.want.Enabled)
			}
			if got.DmaEnabled != tt.want.DmaEnabled {
				t.Errorf("DmaEnabled: got %v, want %v", got.DmaEnabled, tt.want.DmaEnabled)
			}
			if string(got.BusConfig) != string(tt.want.BusConfig) {
				t.Errorf("BusConfig: got %x, want %x", got.BusConfig, tt.want.BusConfig)
			}
			if len(got.TemplateIDs) != len(tt.want.TemplateIDs) {
				t.Errorf("TemplateIDs length: got %d, want %d", len(got.TemplateIDs), len(tt.want.TemplateIDs))
			} else {
				for i, v := range got.TemplateIDs {
					if v != tt.want.TemplateIDs[i] {
						t.Errorf("TemplateIDs[%d]: got %d, want %d", i, v, tt.want.TemplateIDs[i])
					}
				}
			}
		})
	}
}

func TestDecodeChannelEntry_Empty(t *testing.T) {
	got := decodeChannelEntry([]byte{})
	if got.ID != 0 || got.BusType != 0 {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeDmaChannel tests ---

func TestDecodeDmaChannel(t *testing.T) {
	tests := []struct {
		name  string
		build func(enc *frame.Encoder)
		want  models.DmaChannelInfo
	}{
		{
			name: "full DMA channel",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 1)        // dma_id
				enc.EncodeString(2, "DMA_CH0") // name
				enc.EncodeVarint(3, 2)        // dma_type
				enc.EncodeVarint(4, 3)        // capabilities
				enc.EncodeVarint(5, 4096)     // max_burst
				enc.EncodeVarint(6, 0)        // state (free)
				enc.EncodeString(7, "UART0")  // bound_to
				enc.EncodeVarint(8, 1)        // compatible_bus (UART)
			},
			want: models.DmaChannelInfo{
				DmaID: 1, Name: "DMA_CH0", DmaType: 2, Capabilities: 3,
				MaxBurst: 4096, State: 0, BoundTo: "UART0", CompatibleBus: 1,
			},
		},
		{
			name: "DMA allocated state",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 2)
				enc.EncodeVarint(6, 1) // state = allocated
			},
			want: models.DmaChannelInfo{
				DmaID: 2, State: 1,
			},
		},
		{
			name: "DMA minimal",
			build: func(enc *frame.Encoder) {
				enc.EncodeVarint(1, 5)
			},
			want: models.DmaChannelInfo{
				DmaID: 5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildSubFrame(tt.build)
			got := decodeDmaChannel(data)
			if got != tt.want {
				t.Errorf("decodeDmaChannel() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeDmaChannel_Empty(t *testing.T) {
	got := decodeDmaChannel([]byte{})
	if got.DmaID != 0 || got.Name != "" {
		t.Errorf("expected zero-value for empty data, got %+v", got)
	}
}

// --- decodeChannelsBlob multi-channel combo test ---

func TestDecodeChannelsBlob(t *testing.T) {
	ch1 := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeVarint(1, 1)
		enc.EncodeVarint(2, 1) // UART
		enc.EncodeVarint(3, 1)
		enc.EncodeVarint(4, 1000)
		enc.EncodeBool(5, true)
	})
	ch2 := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeVarint(1, 2)
		enc.EncodeVarint(2, 2) // I2C
		enc.EncodeVarint(3, 0x76)
		enc.EncodeVarint(4, 5000)
		enc.EncodeBool(5, true)
		enc.EncodeVarint(7, 1)
		enc.EncodeVarint(7, 2)
	})

	channelsBlob := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeBytes(1, ch1)
		enc.EncodeBytes(1, ch2)
	})

	channels := decodeChannelsBlob(channelsBlob)
	if len(channels) != 2 {
		t.Fatalf("channel count: got %d, want 2", len(channels))
	}

	if channels[0].ID != 1 || channels[0].BusType != 1 {
		t.Errorf("ch0: got %+v", channels[0])
	}
	if channels[1].ID != 2 || channels[1].BusType != 2 {
		t.Errorf("ch1: got %+v", channels[1])
	}
	if channels[1].HardwareID != 0x76 {
		t.Errorf("ch1 HardwareID: got %d, want %d", channels[1].HardwareID, 0x76)
	}
	if len(channels[1].TemplateIDs) != 2 {
		t.Errorf("ch1 TemplateIDs: got %d, want 2", len(channels[1].TemplateIDs))
	}
}

func TestDecodeChannelsBlob_Empty(t *testing.T) {
	channels := decodeChannelsBlob([]byte{})
	if len(channels) != 0 {
		t.Errorf("expected 0 channels for empty blob, got %d", len(channels))
	}
}

func TestDecodeChannelsBlob_SingleChannel(t *testing.T) {
	ch := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeVarint(1, 42)
		enc.EncodeVarint(2, 4) // GPIO
	})

	channelsBlob := buildSubFrame(func(enc *frame.Encoder) {
		enc.EncodeBytes(1, ch)
	})

	channels := decodeChannelsBlob(channelsBlob)
	if len(channels) != 1 {
		t.Fatalf("channel count: got %d, want 1", len(channels))
	}
	if channels[0].ID != 42 || channels[0].BusType != 4 {
		t.Errorf("channel: got %+v", channels[0])
	}
}
