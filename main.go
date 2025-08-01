package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type Config struct {
	ServerAddress string
	BearerToken   string
}

func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	f.StringVar(&cfg.ServerAddress, "host", "", "Server address")
	f.StringVar(&cfg.BearerToken, "token", "", "Bearer token")
}

const streamID = 3

func main() {
	cfg := &Config{}
    cfg.RegisterFlags(flag.CommandLine)
    flag.Parse()

    log.SetFlags(log.Lshortfile|log.Ltime)

    tlsConf := &tls.Config{
         //InsecureSkipVerify: true,
		NextProtos: []string{http2.NextProtoTLS},
    }

    conn, err := tls.Dial("tcp", fmt.Sprintf("%s:443", cfg.ServerAddress), tlsConf)
    if err != nil {
        log.Println(err)
        return
    }
    defer conn.Close()
	log.Println("Connected to", conn.RemoteAddr())

	framer := http2.NewFramer(conn, conn)
	
	log.Println("Sending preface")
	_, err = conn.Write([]byte(http2.ClientPreface))

	if err != nil {
		log.Println(err)
		return
	}

	var f http2.Frame
	f, err = framer.ReadFrame()
	if err != nil {
		log.Println(err)
		return
	}
	log.Println("Got frame", f.Header().Type)
	if f.Header().Type != http2.FrameSettings {
		log.Println("Expected SETTINGS frame")
		return
	}

	log.Println("Sending settings ack")
	err = framer.WriteSettingsAck()
	if err != nil {
		log.Println(err)
		return
	}

	f, err = framer.ReadFrame()
	if err != nil {
		log.Println(err)
		return
	}
	log.Println("Got frame", f.Header().Type)
	if f.Header().Type == http2.FrameWindowUpdate {
		log.Println("Window size", f.(*http2.WindowUpdateFrame).Increment)
		log.Println("Stream id", f.(*http2.WindowUpdateFrame).StreamID)
	} else {
		log.Println("Expected WINDOW_UPDATE frame")
		return
	}

	influxData := "krajotest,bar_label=abc,source=grafana_cloud_docs metric=42.0\n"

	var headers bytes.Buffer
	enc := hpack.NewEncoder(&headers)
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: "https"})
	enc.WriteField(hpack.HeaderField{Name: ":authority", Value: cfg.ServerAddress})
	enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/api/v1/push/influx/write"})
	enc.WriteField(hpack.HeaderField{Name: "user-agent", Value: "krajoramaslowclient/1.0"})
	enc.WriteField(hpack.HeaderField{Name: "accept", Value: "*/*"})
	enc.WriteField(hpack.HeaderField{Name: "authorization", Value: "Bearer " + cfg.BearerToken})
	enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/json"})
	enc.WriteField(hpack.HeaderField{Name: "content-length", Value: fmt.Sprintf("%d", len(influxData))})


	log.Println("Sending headers, length", len(headers.Bytes()), "content-length", len(influxData))
	err = framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: headers.Bytes(),
		EndStream:     false,
		EndHeaders:    true,
	})
	if err != nil {
		log.Println(err)
		return
	}

	wg := &sync.WaitGroup{}

	wg.Add(1)

	processResponses := func() {
		for {
			// Read response
			log.Println("Reading response ----------")
			f, err = framer.ReadFrame()
			if err != nil {
				log.Println(err)
				return
			}
			log.Println("Got frame", f.Header().Type, "stream", f.Header().StreamID)
			switch f.Header().Type {
			case http2.FrameHeaders:
				b := f.(*http2.HeadersFrame).HeaderBlockFragment()
				dec := hpack.NewDecoder(4096, func(hf hpack.HeaderField) {
					log.Println(hf.Name, hf.Value)
				})
				h, err := dec.DecodeFull(b)
				if err != nil {
					log.Println(err)
					return
				}
				log.Println("Decoded headers", h)
			case http2.FrameData:
				d := f.(*http2.DataFrame)
				log.Println("Got data frame", string(d.Data()))
			case http2.FrameGoAway:
				log.Println("Error code", f.(*http2.GoAwayFrame).ErrCode)
				log.Println(string(f.(*http2.GoAwayFrame).DebugData()))
			case http2.FrameRSTStream:
				log.Println("Error code", f.(*http2.RSTStreamFrame).ErrCode)
				log.Println("String", f.(*http2.RSTStreamFrame).String())
				return
			case http2.FramePing:
				log.Println("Ping")
			}
		}
	}

	go func() {
		processResponses()
		wg.Done()
		log.Println("Done reading responses")
	}()

	log.Println("Sending data", influxData)
	for _, c := range influxData {

		part := string(c)
		err = framer.WriteData(streamID, false, []byte(part))
		if err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1*time.Millisecond)
	}

	wg.Wait()

	log.Println("Closing stream")
}