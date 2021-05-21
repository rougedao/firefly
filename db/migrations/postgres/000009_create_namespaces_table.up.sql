BEGIN;
CREATE SEQUENCE namespaces_seq;
CREATE TABLE namespaces (
  id          UUID            NOT NULL PRIMARY KEY,
  seq         BIGINT          NOT NULL DEFAULT nextval('namespaces_seq'),
  name        VARCHAR(64)     NOT NULL,
  ntype       VARCHAR(64)     NOT NULL,
  description VARCHAR(4096),
  created     BIGINT          NOT NULL,
  confirmed   BIGINT
);

CREATE UNIQUE INDEX namespaces_sequence ON operations(seq);
CREATE UNIQUE INDEX namespaces_name ON namespaces(name);

COMMIT;