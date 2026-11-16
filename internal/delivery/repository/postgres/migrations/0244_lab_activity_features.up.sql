-- What the laboratory said (contract 2.6) about the group's activity reports when the group was last pulled:
--   device_touch     the cluster produces device_touch events (iptables VPN firewall). False: the VPN bytes and the devices-touched
--                    figures of its labs are "not reported" (null), never zero.
--   console_max_s    the longest a console session lives there (the chart's terminal.limits.maxLifetime); 0: not told, the
--                    daemon's own setting caps a session that never reported its end.
ALTER TABLE lab_activity_cursors
    ADD COLUMN device_touch boolean NOT NULL DEFAULT false,
    ADD COLUMN console_max_s integer NOT NULL DEFAULT 0 CHECK (console_max_s >= 0);
