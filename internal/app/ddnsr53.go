package app

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/crazy-max/ddns-route53/internal/config"
	"github.com/crazy-max/ddns-route53/pkg/identme"
	"github.com/hako/durafmt"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"
)

// Client represents an active ddns-route53 object
type Client struct {
	cfg      *config.Configuration
	loc      *time.Location
	cron     *cron.Cron
	r53      *route53.Route53
	im       *identme.Client
	jobID    cron.EntryID
	lastIPv4 net.IP
	lastIPv6 net.IP
	locker   uint32
}

// New creates new ddns-route53 instance
func New(cfg *config.Configuration, loc *time.Location) (*Client, error) {
	// Static credentials
	creds := credentials.NewStaticCredentials(
		cfg.Credentials.AccessKeyID,
		cfg.Credentials.SecretAccessKey,
		"",
	)

	// AWS SDK session
	sess, err := session.NewSession()
	if err != nil {
		return nil, err
	}

	return &Client{
		cfg: cfg,
		loc: loc,
		cron: cron.New(cron.WithLocation(loc), cron.WithParser(cron.NewParser(
			cron.SecondOptional|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor),
		)),
		r53: route53.New(sess, &aws.Config{Credentials: creds}),
		im: identme.NewClient(
			fmt.Sprintf("ddns-route53/%s go/%s %s", cfg.App.Version, runtime.Version()[2:], strings.Title(runtime.GOOS)),
			cfg.Cli.MaxRetries,
		),
	}, nil
}

// Start starts ddns-route53
func (c *Client) Start() error {
	var err error

	// Run on startup
	c.Run()

	// Check scheduler enabled
	if c.cfg.Cli.Schedule == "" {
		return nil
	}

	// Init scheduler
	c.jobID, err = c.cron.AddJob(c.cfg.Cli.Schedule, c)
	if err != nil {
		return err
	}
	log.Info().Msgf("Cron initialized with schedule %s", c.cfg.Cli.Schedule)

	// Start scheduler
	c.cron.Start()
	log.Info().Msgf("Next run in %s (%s)",
		durafmt.ParseShort(c.cron.Entry(c.jobID).Next.Sub(time.Now())).String(),
		c.cron.Entry(c.jobID).Next)

	select {}
}

// Run runs ddns-route53 process
func (c *Client) Run() {
	var err error
	if !atomic.CompareAndSwapUint32(&c.locker, 0, 1) {
		log.Warn().Msg("Already running")
		return
	}
	defer atomic.StoreUint32(&c.locker, 0)
	if c.jobID > 0 {
		defer log.Info().Msgf("Next run in %s (%s)",
			durafmt.ParseShort(c.cron.Entry(c.jobID).Next.Sub(time.Now())).String(),
			c.cron.Entry(c.jobID).Next)
	}

	var wanIPv4 net.IP
	if c.cfg.Route53.HandleIPv4 {
		wanIPv4, err = c.im.IPv4()
		if err != nil {
			log.Error().Err(err).Msg("Cannot retrieve WAN IPv4 address")
		} else {
			log.Info().Msgf("Current WAN IPv4: %s", wanIPv4)
		}
	}

	var wanIPv6 net.IP
	if c.cfg.Route53.HandleIPv6 {
		wanIPv6, err = c.im.IPv6()
		if err != nil {
			log.Error().Err(err).Msg("Cannot retrieve WAN IPv6 address")
		} else {
			log.Info().Msgf("Current WAN IPv6: %s", wanIPv6)
		}
	}

	if wanIPv4 == nil && wanIPv6 == nil {
		return
	}

	// Skip if last IP is identical or empty
	if wanIPv4.Equal(c.lastIPv4) && wanIPv6.Equal(c.lastIPv6) {
		log.Info().Msg("WAN IPv4/IPv6 addresses have not changed since last update. Skipping...")
		return
	}

	// Create Route53 changes
	r53Changes := make([]*route53.Change, len(c.cfg.Route53.RecordsSet))
	for i, rs := range c.cfg.Route53.RecordsSet {
		if wanIPv4 == nil && rs.Type == route53.RRTypeA {
			log.Error().Msgf("No WAN IPv4 address available to update %s record", rs.Name)
			continue
		} else if wanIPv6 == nil && rs.Type == route53.RRTypeAaaa {
			log.Error().Msgf("No WAN IPv6 address available to update %s record", rs.Name)
			continue
		}
		recordValue := aws.String(wanIPv4.String())
		if rs.Type == route53.RRTypeAaaa {
			recordValue = aws.String(wanIPv6.String())
		}
		r53Changes[i] = &route53.Change{
			Action: aws.String("UPSERT"),
			ResourceRecordSet: &route53.ResourceRecordSet{
				Name:            aws.String(rs.Name),
				Type:            aws.String(rs.Type),
				TTL:             aws.Int64(rs.TTL),
				ResourceRecords: []*route53.ResourceRecord{{Value: recordValue}},
			},
		}
	}

	// Check changes
	if len(r53Changes) == 0 {
		log.Warn().Msgf("No Route53 record set to update. Skipping...")
		return
	}

	// Create resource records
	resRS := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Comment: aws.String(fmt.Sprintf("Updated by %s %s at %s",
				c.cfg.App.Name,
				c.cfg.App.Version,
				time.Now().In(c.loc).Format("2006-01-02 15:04:05"),
			)),
			Changes: r53Changes,
		},
		HostedZoneId: aws.String(c.cfg.Route53.HostedZoneID),
	}

	// Update records
	resp, err := c.r53.ChangeResourceRecordSets(resRS)
	if err != nil {
		log.Error().Err(err).Msg("Cannot update records set")
	}
	log.Info().Interface("changes", resp).Msgf("%d records set updated", len(c.cfg.Route53.RecordsSet))

	// Update last IPv4/IPv6
	c.lastIPv4 = wanIPv4
	c.lastIPv6 = wanIPv6
}

// Close closes ddns-route53
func (c *Client) Close() {
	if c.cron != nil {
		c.cron.Stop()
	}
}
