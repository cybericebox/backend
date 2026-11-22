-- Several opponents of a variant may play in one lab: each run has its own tools device there, and tools_index is the index of
-- that device in its lab (its control host is ControlHost(tools_index), the one address the run's VPN client may reach). The runs
-- of one lab share its deploy_id; the lab is destroyed when the last of them ends. The default 0 is the one device of a lab
-- made for one opponent (host .10), so the runs from before keep their address.
ALTER TABLE assistant_runs
    ADD COLUMN tools_index integer NOT NULL DEFAULT 0 CHECK (tools_index >= 0);

CREATE INDEX assistant_runs_deploy_active ON assistant_runs (deploy_id) WHERE state IN ('starting', 'running', 'closing');
