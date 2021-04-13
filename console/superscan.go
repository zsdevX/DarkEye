package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/schollz/progressbar"
	"github.com/zsdevX/DarkEye/common"
	"github.com/zsdevX/DarkEye/superscan"
	"github.com/zsdevX/DarkEye/superscan/plugins"
	"golang.org/x/time/rate"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type superScanRuntime struct {
	Module
	parent *RequestContext

	IpList                string
	PortList              string
	TimeOut               int
	Thread                int
	Plugin                string
	PacketPerSecond       int
	UserList              string
	PassList              string
	ActivePort            string
	OnlyCheckAliveNetwork bool
	OnlyCheckAliveHost    bool

	MaxConcurrencyIp int
	Bar              *progressbar.ProgressBar
	PacketRate       *rate.Limiter
	scan             *superscan.Scan
	flagSet          *flag.FlagSet
	sync.RWMutex
}

var (
	superScan               = "superScan"
	superScanRuntimeOptions = &superScanRuntime{
		flagSet: flag.NewFlagSet(superScan, flag.ExitOnError),
	}
)

func (s *superScanRuntime) Start(parent context.Context) {
	s.initializer(parent)

	if s.OnlyCheckAliveNetwork || s.OnlyCheckAliveHost {
		scan := s.newScan("")
		scan.PingNet(s.IpList, s.OnlyCheckAliveHost)
		return
	}
	//解析变量
	if s.IpList == "$IP" {
		ipList := analysisRuntimeOptions.ipVar("")
		if len(ipList) == 0 {
			common.Log("superScan.start", "$IP未检索到目标", common.INFO)
			return
		}
		s.IpList = ""
		for _, v := range ipList {
			s.IpList += v + ","
		}
		s.IpList = strings.TrimSuffix(s.IpList, ",")
	}
	//初始化scan对象
	ips := strings.Split(s.IpList, ",")
	tot := 0
	scans := make([]*superscan.Scan, 0)
	for _, ip := range ips {
		base, start, end, err := common.GetIPRange(ip)
		if err != nil {
			common.Log(s.parent.CmdArgs[0], err.Error(), common.FAULT)
			return
		}
		for {
			nip := common.GenIP(base, start)
			if common.CompareIP(nip, end) > 0 {
				break
			}
			s := s.newScan(nip)
			s.ActivePort = "0"
			s.Parent = parent
			_, t := common.GetPortRange(s.PortRange)
			tot += t
			scans = append(scans, s)
			start++
		}
	}
	fmt.Println(fmt.Sprintf(
		"已加载%d个IP,共计%d个端口,启动每IP扫描端口线程数%d,同时可同时检测IP数量%d",
		len(scans), tot, s.Thread, s.MaxConcurrencyIp))
	plugins.SupportPlugin()

	//建立进度条
	s.Bar = s.newBar(tot)
	if len(scans) == 1 {
		//单IP支持校正
		scans[0].ActivePort = s.ActivePort
	}
	task := common.NewTask(s.MaxConcurrencyIp, parent)
	defer task.Wait("superScan")
	for _, sc := range scans {
		//Job
		if !task.Job() {
			break
		}
		go func(sup *superscan.Scan) {
			defer task.UnJob()
			sup.Run()
		}(sc)
	}
}

func (s *superScanRuntime) Init(requestContext *RequestContext) {
	superScanRuntimeOptions.parent = requestContext
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.IpList, "ip", "127.0.0.1", "a.b.c.1-a.b.c.255")
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.PortList, "port-list", common.PortList, "端口范围,默认1000+常用端口")
	superScanRuntimeOptions.flagSet.IntVar(&superScanRuntimeOptions.TimeOut, "timeout", 3000, "网络超时请求(单位ms)")
	superScanRuntimeOptions.flagSet.IntVar(&superScanRuntimeOptions.Thread, "thread", 128, "每个IP爆破端口的线程数量")
	superScanRuntimeOptions.flagSet.IntVar(&superScanRuntimeOptions.PacketPerSecond, "pps", 0, "扫描工具整体发包频率 packets/秒")
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.Plugin, "plugin", "", "指定协议插件爆破")
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.UserList, "user-list", "", "字符串(u1,u2,u3)或文件(一个账号一行）")
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.PassList, "pass-list", "", "字符串(p1,p2,p3)或文件（一个密码一行")
	superScanRuntimeOptions.flagSet.StringVar(&superScanRuntimeOptions.ActivePort, "alive_port", "0", "使用已知开放的端口校正扫描行为。例如某服务器限制了IP访问频率，开启此功能后程序发现限制会自动调整保证扫描完整、准确")
	superScanRuntimeOptions.flagSet.BoolVar(&superScanRuntimeOptions.OnlyCheckAliveNetwork, "only-alive-network", false, "只检查活跃主机的网段(ping)")
	superScanRuntimeOptions.flagSet.BoolVar(&superScanRuntimeOptions.OnlyCheckAliveHost, "alive-host-check", false, "检查所有活跃主机(ping)")
	superScanRuntimeOptions.MaxConcurrencyIp = 32
}

func (s *superScanRuntime) ValueCheck(value string) (bool, error) {
	if v, ok := superScanValueCheck[value]; ok {
		if isDuplicateArg(value, s.parent.CmdArgs) {
			return false, fmt.Errorf("参数重复")
		}
		return v, nil
	}
	return false, fmt.Errorf("无此参数")
}

func (a *superScanRuntime) CompileArgs(cmd []string) error {
	if err := a.flagSet.Parse(splitCmd(cmd)); err != nil {
		return err
	}
	a.flagSet.Parsed()
	return nil
}

func (a *superScanRuntime) Usage() {
	fmt.Println(fmt.Sprintf("Usage of %s:", superScan))
	fmt.Println("Options:")
	a.flagSet.VisitAll(func(f *flag.Flag) {
		var opt = "  -" + f.Name
		fmt.Println(opt)
		fmt.Println(fmt.Sprintf("		%v (default '%v')", f.Usage, f.DefValue))
	})
}

func (s *superScanRuntime) newScan(ip string) *superscan.Scan {
	return &superscan.Scan{
		Ip:          ip,
		TimeOut:     superScanRuntimeOptions.TimeOut,
		ActivePort:  superScanRuntimeOptions.ActivePort,
		PortRange:   superScanRuntimeOptions.PortList,
		Thread:      superScanRuntimeOptions.Thread,
		Callback:    s.myCallback,
		BarCallback: s.myBarCallback,
	}
}

func (s *superScanRuntime) initializer(parent context.Context) {
	//设置自定义文件字典替代内置字典
	if s.UserList != "" {
		if _, e := os.Stat(s.UserList); e != nil {
			plugins.Config.UserList = common.GenDicFromFile(s.UserList)
		} else {
			plugins.Config.UserList = strings.Split(s.UserList, ",")
		}
	}
	if s.PassList != "" {
		if _, e := os.Stat(s.PassList); e != nil {
			plugins.Config.PassList = common.GenDicFromFile(s.PassList)
		} else {
			plugins.Config.PassList = strings.Split(s.PassList, ",")
		}
	}
	//设置发包频率
	if s.PacketPerSecond > 0 {
		//每秒发包*mRateLimiter，缓冲10个
		s.PacketRate = rate.NewLimiter(rate.Every(1000000*time.Microsecond/time.Duration(s.PacketPerSecond)), 10)
	}
	plugins.Config.PPS = s.PacketRate
	plugins.Config.SelectPlugin = s.Plugin
	plugins.Config.ParentCtx = parent
	plugins.Config.TimeOut = s.TimeOut
}

func (s *superScanRuntime) newBar(max int) *progressbar.ProgressBar {
	bar := progressbar.NewOptions(max,
		progressbar.OptionSetDescription("[Cracking ...]"),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionOnCompletion(func() {
			_, _ = fmt.Fprint(os.Stderr, "\n扫描任务完成")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
		progressbar.OptionFullWidth(),
	)
	_ = bar.RenderBlank()
	return bar
}

func (s *superScanRuntime) myCallback(a interface{}) {
	plg := a.(*plugins.Plugins)
	ent := analysisEntity{
		Ip:      plg.TargetIp,
		Port:    plg.TargetPort,
		Service: plg.Result.ServiceName,
		Os:      plg.Result.NetBios.Os,
		NetBios: fmt.Sprintf(
			"[Ip:'%s' Shares:'%s']", plg.Result.NetBios.Ip, plg.Result.NetBios.Shares),
		Url:             plg.Result.Web.Url,
		Title:           plg.Result.Web.Title,
		WebServer:       plg.Result.Web.Server,
		WebResponseCode: plg.Result.Web.Code,
		WeakAccount: fmt.Sprintf(
			"[%s/%s]", plg.Result.Cracked.Username, plg.Result.Cracked.Password),
	}

	message := ent.Service
	if ent.Title != "" {
		message += fmt.Sprintf(" ['%s' '%s' '%d' '%s']", ent.Title, ent.WebServer, ent.WebResponseCode, ent.Url)
	}
	if plg.Result.NetBios.Ip != "" ||
		plg.Result.NetBios.Os != "" ||
		plg.Result.NetBios.Shares != "" {
		message += fmt.Sprintf(" ['%s' '%s' '%s']",
			plg.Result.NetBios.Ip, plg.Result.NetBios.Os, plg.Result.NetBios.Shares)
	}
	if plg.Result.Cracked.Username != "" || plg.Result.Cracked.Password != "" {
		message += fmt.Sprintf(" crack:['%s' '%s']",
			plg.Result.Cracked.Username, plg.Result.Cracked.Password)
	}
	common.Log(net.JoinHostPort(ent.Ip, ent.Port)+"[Opened]", message, common.INFO)
	analysisRuntimeOptions.createOrUpdate(&ent)
}

func (s *superScanRuntime) myBarCallback(i int) {
	_ = s.Bar.Add(i)
}
