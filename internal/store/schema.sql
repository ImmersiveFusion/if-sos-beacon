-- Azure SQL / SQL Server schema for the sos-beacon persistence port.
-- Executed idempotently on every open (IF NOT EXISTS guards), as a single
-- T-SQL batch (statements separated by semicolons; no GO separators).

IF NOT EXISTS (SELECT 1 FROM sys.tables WHERE name = 'seen')
    CREATE TABLE seen (
        source NVARCHAR(64)  NOT NULL,
        id     NVARCHAR(256) NOT NULL,
        CONSTRAINT pk_seen PRIMARY KEY (source, id)
    );

IF NOT EXISTS (SELECT 1 FROM sys.tables WHERE name = 'finding')
    CREATE TABLE finding (
        beacon     NVARCHAR(128) NOT NULL,
        source     NVARCHAR(64)  NOT NULL,
        id         NVARCHAR(256) NOT NULL,
        data       NVARCHAR(MAX) NOT NULL,
        created_at DATETIME2     NOT NULL CONSTRAINT df_finding_created DEFAULT SYSUTCDATETIME(),
        CONSTRAINT pk_finding PRIMARY KEY (beacon, source, id)
    );

IF NOT EXISTS (SELECT 1 FROM sys.tables WHERE name = 'claim')
    CREATE TABLE claim (
        finding_id NVARCHAR(512) NOT NULL,
        claimant   NVARCHAR(256) NOT NULL,
        ts         DATETIME2     NOT NULL CONSTRAINT df_claim_ts DEFAULT SYSUTCDATETIME(),
        CONSTRAINT pk_claim PRIMARY KEY (finding_id)
    );
