-- event_mask_email masks an address as dispatch.MaskEmail does: the first character of the local part, «***» and
-- the domain after the last «@» («olena@example.com» → «o***@example.com», «@example.com» → «***@example.com»); a
-- text without «@» is returned as it is. The manage lists give it to a read-only moderator, who must not filter or
-- sort by more than the screen shows.
CREATE OR REPLACE FUNCTION event_mask_email(p_email text)
    RETURNS text
    LANGUAGE sql
    IMMUTABLE
    PARALLEL SAFE
AS
$$
SELECT regexp_replace(p_email, '^(.?).*(@[^@]*)$', '\1***\2')
$$;
