-- No-op: 000057 only dropped sticky values that the pre-2.2.0 renderer never
-- emitted for failover backends; restoring them would change nothing rendered
-- by the old renderer and cannot be reconstructed exactly.
SELECT 1;
