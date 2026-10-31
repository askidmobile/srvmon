package aiagent

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	readOnly := []string{
		"docker ps -a",
		"docker logs --tail 50 crm-scheduler",
		"docker logs --since 1h crm-api | grep -i error | tail -20",
		"docker compose -f /srv/crm/compose.yml ps",
		"docker inspect -f '{{.State.OOMKilled}}' crm-scheduler",
		"docker stats --no-stream",
		"docker container ls",
		"docker system df",
		"docker events --since 12h --filter container=crm-scheduler --until 0s",
		"df -h",
		"free -m",
		"uptime",
		"cat /etc/os-release",
		"tail -n 100 /var/log/syslog",
		"journalctl -u nginx --since '1 hour ago' --no-pager",
		"systemctl status nginx",
		"systemctl --no-pager list-units --failed",
		"ps aux | sort -k3 -nr | head",
		"ss -tlnp",
		"ip addr show",
		"ip -4 route",
		"find /var/log -name '*.log' -size +100M",
		"top -bn1 | head -20",
		"grep \"error\" /var/log/syslog",
		"dmesg -T | tail",
		"dmesg -T -l err,warn",
		"ls -la /srv",
		"uniq -c",
		"sort -t, -k2 -n file",
		"sort -k 3 -rn",
		"sort --key=2 --numeric-sort -u",
		"date",
		"date -u +%s",
		"date -d yesterday +%F",
		"date --iso-8601=seconds",
		"hostname",
		"hostname -I",
		"ss -s",
		"sensors",
		"file /bin/ls",
		"journalctl -b -1 -p err --no-pager -n 50",
		"journalctl -xeu nginx",
		"journalctl _SYSTEMD_UNIT=sshd.service -o json-pretty -n20",
		"journalctl --since=today --disk-usage",
		"systemctl --user status app.service",
		"systemctl show -p ActiveState nginx",
		"systemctl list-units --type=service --state=running",
		"timedatectl",
		"docker compose -f /srv/app/compose.yml config",
		"docker compose -p app ps -a",
		"docker logs --tail 100 -t app",
	}
	for _, c := range readOnly {
		if ok, why := Classify(c); !ok {
			t.Errorf("want read-only: %q (%s)", c, why)
		}
	}
	mutating := []string{
		"docker restart crm-scheduler",
		"docker compose up -d",
		"docker compose -f x.yml down",
		"docker rm -f x",
		"docker system prune -af",
		"docker container rm x",
		"docker exec x rm -rf /",
		"docker stats",
		"docker -H tcp://x ps",
		"rm -rf /tmp/x",
		"sudo cat /etc/shadow",
		"cat /etc/passwd > /tmp/x",
		"echo hi >> /etc/motd",
		"ls; rm x",
		"ls && rm x",
		"ls || rm x",
		"ls & rm x",
		"cat $(echo /etc/shadow)",
		"cat `id`",
		"cat \"$HOME/.bashrc\"",
		"FOO=1 ls",
		"/bin/rm x",
		"systemctl restart nginx",
		"journalctl --vacuum-time=1d",
		"journalctl --vacuum-size=1G",
		"find / -name x -delete",
		"find / -exec rm {} ;",
		"sort -o /etc/passwd x",
		"sort -no /etc/passwd x",
		"journalctl --cursor-file=/home/srvmon/.ssh/authorized_keys",
		"journalctl --cursor-file /tmp/x",
		"journalctl --vacuum-time=1d -n5",
		"journalctl --rotate",
		"journalctl -D /tmp --flush",
		"systemctl --user --property status stop app.service",
		"systemctl --root=/ status enable x",
		"systemctl -H host status",
		"systemctl status --runtime x",
		"timedatectl set-timezone UTC",
		"docker compose -f /srv/app/compose.yml config --output /home/srvmon/.ssh/authorized_keys",
		"docker compose config -o x",
		"docker compose config --output=x",
		"docker compose --dry-run ps",
		"sort --output=/etc/passwd x",
		"sort -S 1b --compress-program=sh",
		"sort --compress-program sh",
		"sort -T /tmp x",
		"date -s 2020-01-01",
		"date --set=2020-01-01",
		"date -us 10:00",
		"date 010100002020",
		"hostname evil",
		"hostname -F /tmp/name",
		"ss -K dst 1.2.3.4",
		"ss --kill",
		"ss -D /tmp/x",
		"sensors -s",
		"file -C -m x",
		"uniq in out",
		"dmesg -c",
		"dmesg -n 1",
		"dmesg --clear",
		"dmesg -TC",
		"ip link set eth0 down",
		"ip route add default via 1.2.3.4",
		"top",
		"sed -i s/a/b/ file",
		"awk 'BEGIN{system(\"id\")}'",
		"curl -X POST http://x",
		"apt-get upgrade -y",
		"kill -9 1",
		"tee /etc/x",
		"xargs rm",
		"ls | xargs rm",
		"cat 'unterminated",
		"",
		"ls\nrm x",
		"ls # comment",
	}
	for _, c := range mutating {
		if ok, _ := Classify(c); ok {
			t.Errorf("want confirmation: %q", c)
		}
	}
}

func TestSchedule(t *testing.T) {
	base := time.Date(2026, 10, 31, 8, 7, 30, 0, time.UTC)
	cases := []struct {
		expr string
		want time.Time
	}{
		{"*/15 * * * *", time.Date(2026, 10, 31, 8, 15, 0, 0, time.UTC)},
		{"0 * * * *", time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"30 9 * * 1-5", time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)}, // Oct 31 2026 is Saturday
		{"0 0 1 * *", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"0 12 29 2 *", time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)},
		{"0 0 * * 7", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}, // 7 = Sunday
		{"@every 30m", base.Add(30 * time.Minute)},
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Next(base); !got.Equal(c.want) {
			t.Errorf("%s: next %v, want %v", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"* * * * *", "*/2 * * * *", "@every 1m", "61 * * * *", "* * *", "a b c d e", "5-1 * * * *", "*/0 * * * *", "0 0 31 2 *", "0 0 30 2 *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}
