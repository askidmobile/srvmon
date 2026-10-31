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
		"ls -la /srv",
		"uniq -c",
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
		"uniq in out",
		"dmesg -c",
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
