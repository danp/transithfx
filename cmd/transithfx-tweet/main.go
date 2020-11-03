package main

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	_ "image/png"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/golang/freetype/truetype"
	"github.com/joeshaw/envdecode"
	"golang.org/x/text/message"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

func main() {
	var cfg struct {
		TwitterConsumerKey    string `env:"TWITTER_CONSUMER_KEY,required"`
		TwitterConsumerSecret string `env:"TWITTER_CONSUMER_SECRET,required"`
		TwitterAppToken       string `env:"TWITTER_APP_TOKEN,required"`
		TwitterAppSecret      string `env:"TWITTER_APP_SECRET,required"`

		TestMode bool `env:"TEST_MODE"`
	}
	if err := envdecode.Decode(&cfg); err != nil {
		log.Fatal(err)
	}

	data, err := fetch()
	if err != nil {
		log.Fatal("fetching:", err)
	}
	if len(data) == 0 {
		return
	}

	want := 8 // ideally show last 8 weeks
	if len(data) < want {
		want = len(data)
	}
	data = data[len(data)-want:]
	lastWeek := data[len(data)-1]

	already, err := isAlreadyDone(lastWeek)
	if err != nil {
		log.Fatal("checking if already done:", err)
	}
	if already {
		log.Println("already done for", lastWeek.end)
		return
	}

	gb, err := graph(data)
	if err != nil {
		log.Fatal("graphing:", err)
	}

	p := message.NewPrinter(message.MatchLanguage("en"))
	stxt := p.Sprintf("Week ending %s had %d passengers", lastWeek.end.Format("Mon Jan 02"), lastWeek.count)
	atxt := altText(data, p)
	log.Printf("at=tweet stxt=%q len=%d atxt=%q", stxt, len(stxt), atxt)

	if cfg.TestMode {
		log.Println("test mode, writing graph.png")
		if err := ioutil.WriteFile("graph.png", gb, 0600); err != nil {
			log.Fatal("writing graph:", err)
		}

		log.Println("test mode, not tweeting")
		return
	}

	oaConfig := oauth1.NewConfig(cfg.TwitterConsumerKey, cfg.TwitterConsumerSecret)
	oaToken := oauth1.NewToken(cfg.TwitterAppToken, cfg.TwitterAppSecret)
	cl := oaConfig.Client(oauth1.NoContext, oaToken)

	mid, err := uploadMedia(cl, gb, "")
	if err != nil {
		log.Fatal("uploading graph as media:", err)
	}

	twc := twitter.NewClient(cl)
	tw, _, err := twc.Statuses.Update(stxt, &twitter.StatusUpdateParams{MediaIds: []int64{mid}})
	if err != nil {
		log.Fatal("creating status update:", err)
	}
	fmt.Println("https://twitter.com/" + tw.User.ScreenName + "/status/" + tw.IDStr)

	if err := markDone(lastWeek); err != nil {
		log.Fatal("marking done:", err)
	}
}

func altText(data []week, p *message.Printer) string {
	out := p.Sprintf("Bar chart of passengers by week for last %d weeks.", len(data))
	if len(data) > 1 {
		curWeek := data[len(data)-1]
		prevWeek := data[len(data)-2]
		var moreOrFewer string
		var pct int
		if curWeek.count > prevWeek.count {
			moreOrFewer = "more"
			pct = int(float64(curWeek.count-prevWeek.count) / float64(prevWeek.count) * 100.0)
		} else {
			moreOrFewer = "fewer"
			pct = int(float64(prevWeek.count-curWeek.count) / float64(prevWeek.count) * 100.0)
		}
		if pct > 0 {
			out += p.Sprintf(" The most recent count of %d is %d%% %s than the previous week.", curWeek.count, pct, moreOrFewer)
		} else {
			out += p.Sprintf(" The most recent count of %d is about the same as the previous week.", curWeek.count)
		}
	}
	return out
}

type week struct {
	start time.Time
	end   time.Time
	count int
}

func fetch() ([]week, error) {
	resp, err := http.Get("https://opendata.arcgis.com/datasets/a0ece3efdc7144d69cb1881b90cd93fe_0.csv")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bad status %d", resp.StatusCode)
	}

	cr := csv.NewReader(resp.Body)
	hdrr, err := cr.Read()
	if err != nil {
		return nil, err
	}
	hdr := make(map[string]int)
	for i, h := range hdrr {
		hdr[h] = i
	}

	weeks := make(map[string]int)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		wr := rec[hdr["Week_Range"]]

		rt := rec[hdr["Ridership_Total"]]
		c, err := strconv.Atoi(rt)
		if err != nil {
			return nil, fmt.Errorf("invalid ridership %q", rt)
		}

		weeks[wr] += c
	}

	out := make([]week, 0, len(weeks))
	for wr, c := range weeks {
		wrf := strings.Fields(wr)
		if len(wrf) != 3 {
			return nil, fmt.Errorf("invalid week range %q", wr)
		}

		sdt, err := time.Parse("2006.01.02", wrf[0])
		if err != nil {
			return nil, fmt.Errorf("invalid week start %q", wrf[0])
		}
		edt, err := time.Parse("2006.01.02", wrf[2])
		if err != nil {
			return nil, fmt.Errorf("invalid week end %q", wrf[2])
		}

		out = append(out, week{start: sdt, end: edt, count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })

	return out, nil
}

func graph(data []week) ([]byte, error) {
	td, err := ioutil.TempDir("", "graph-font")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(td)

	//go:embed Arial.ttf
	var arial []byte
	fontTTF, err := truetype.Parse(arial)
	if err != nil {
		return nil, err
	}
	const fontName = "Arial"
	vg.AddFont(fontName, fontTTF)
	plot.DefaultFont = fontName
	plotter.DefaultFont = fontName

	xs := make([]string, 0, len(data))
	counts := make(plotter.Values, 0, len(data))
	for _, w := range data {
		xs = append(xs, w.end.Format("Jan 02"))
		counts = append(counts, float64(w.count))
	}

	min, max := plotter.Range(counts)

	p, err := plot.New()
	if err != nil {
		return nil, err
	}

	p.Title.Text = "Halifax Transit passengers by week ending"
	p.Y.Max = float64(int(max * 1.05))
	p.Y.Label.Text = "Passengers"
	p.Y.Label.Padding = vg.Length(5)
	p.Y.Tick.Marker = plot.TickerFunc(thousandTicker(p.Y.Tick.Marker))

	w := vg.Points(30)

	barsA, err := NewBarChart(counts, w)
	if err != nil {
		return nil, err
	}
	barsA.YMin = float64(int(min * 0.95))
	barsA.LineStyle.Width = vg.Length(0)

	p.Add(barsA)
	p.NominalX(xs...)

	var buf bytes.Buffer
	wt, err := p.WriterTo(6*vg.Inch, 3*vg.Inch, "png")
	if err != nil {
		return nil, err
	}

	if _, err := wt.WriteTo(&buf); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(&buf)
	if err != nil {
		return nil, err
	}

	bnds := img.Bounds()
	const padding = 20
	outRect := image.Rect(bnds.Min.X-padding, bnds.Min.Y-padding, bnds.Max.X+padding, bnds.Max.Y+padding)
	out := image.NewRGBA(outRect)
	draw.Draw(out, out.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.ZP, draw.Src)
	draw.Draw(out, img.Bounds(), img, image.Pt(outRect.Min.X+padding, outRect.Min.Y+padding), draw.Over)

	buf.Reset()
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func thousandTicker(t plot.Ticker) func(min, max float64) []plot.Tick {
	return func(min, max float64) []plot.Tick {
		tt := t.Ticks(min, max)
		for i := range tt {
			if tt[i].Label == "" || tt[i].Value < 1000 {
				continue
			}
			tt[i].Label = fmt.Sprintf("%dk", int(tt[i].Value/1000))
		}
		return tt
	}
}

func uploadMedia(cl *http.Client, m []byte, altText string) (int64, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)

	fw, err := w.CreateFormField("media")
	if err != nil {
		return 0, err
	}
	if _, err := fw.Write(m); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}

	req, err := http.NewRequest("POST", "https://upload.twitter.com/1.1/media/upload.json", &b)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("got status %d", resp.StatusCode)
	}

	var mresp struct {
		MediaID int64 `json:"media_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&mresp); err != nil {
		return 0, err
	}

	if altText == "" {
		return mresp.MediaID, nil
	}

	var reqb struct {
		MediaID string `json:"media_id"`
		AltText struct {
			Text string `json:"text"`
		} `json:"alt_text"`
	}
	reqb.MediaID = strconv.FormatInt(mresp.MediaID, 10)
	reqb.AltText.Text = altText

	rb, err := json.Marshal(reqb)
	if err != nil {
		return 0, err
	}

	req, err = http.NewRequest("POST", "https://upload.twitter.com/1.1/media/metadata/create.json", bytes.NewReader(rb))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err = cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("got status %d", resp.StatusCode)
	}

	return mresp.MediaID, nil
}

func isAlreadyDone(week week) (bool, error) {
	dc, err := ioutil.ReadFile(".doneWeek")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return string(dc) == week.end.Format("2006-01-02"), nil
}

func markDone(week week) error {
	return ioutil.WriteFile(".doneWeek", []byte(week.end.Format("2006-01-02")), 0600)
}
