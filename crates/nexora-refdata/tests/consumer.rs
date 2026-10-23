use std::fs;
use std::path::PathBuf;

use nexora_refdata::{
    Consumer, RefDataError, SCHEMA_VERSION, Snapshot, SnapshotDocument, verify_and_parse,
};

fn fixture_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../contracts/fixtures/reference-data/v1/snapshot.json")
}

fn hash_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../contracts/fixtures/reference-data/v1/snapshot.sha256")
}

fn fixture() -> (Vec<u8>, String) {
    let bytes = fs::read(fixture_path()).expect("read golden snapshot");
    let hash = fs::read_to_string(hash_path())
        .expect("read golden hash")
        .trim()
        .to_string();
    (bytes, hash)
}

#[test]
fn verifies_and_validates_cross_language_fixture() {
    let (bytes, hash) = fixture();
    let snapshot = verify_and_parse(&bytes, &hash, 1, 1_767_225_600, "default", false)
        .expect("fixture must verify");
    assert_eq!(snapshot.document().schema_version, SCHEMA_VERSION);
    assert_eq!(snapshot.document().assets.len(), 3);
    assert_eq!(snapshot.document().instruments.len(), 4);
    assert_eq!(snapshot.document().margin_tier_sets.len(), 2);
    assert!(!snapshot.is_rollback());

    // Every core instrument independently satisfies the B02 primitives.
    for record in &snapshot.document().instruments {
        record.core.validate().expect("instrument validates");
    }

    // ETH's max withdrawal proves the contract carries more than i64 atomic
    // units without precision loss.
    let eth = snapshot
        .document()
        .assets
        .iter()
        .find(|asset| asset.symbol == "ETH")
        .expect("ETH fixture");
    assert_eq!(eth.max_withdrawal, Some(1_000_000_000_000_000_000_000_i128));
}

#[test]
fn rejects_tampered_bytes_and_bad_hashes() {
    let (bytes, hash) = fixture();
    let mut tampered = bytes.clone();
    tampered.push(b' ');
    assert!(matches!(
        verify_and_parse(&tampered, &hash, 1, 0, "default", false),
        Err(RefDataError::HashMismatch)
    ));
    assert!(matches!(
        verify_and_parse(&bytes, "not-a-hash", 1, 0, "default", false),
        Err(RefDataError::InvalidHash)
    ));
}

#[test]
fn rejects_unknown_schema_and_unknown_top_level_fields() {
    let (bytes, hash) = fixture();
    let document: SnapshotDocument = serde_json::from_slice(&bytes).expect("parse fixture");
    assert_eq!(document.schema_version, SCHEMA_VERSION);

    let mut value: serde_json::Value = serde_json::from_slice(&bytes).expect("parse value");
    value["schema_version"] = serde_json::Value::String("nexora.reference-data.v999".to_string());
    let changed = serde_json::to_vec(&value).expect("encode changed");
    let changed_hash = {
        use sha2::{Digest, Sha256};
        let digest = Sha256::digest(&changed);
        digest
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>()
    };
    assert!(matches!(
        verify_and_parse(&changed, &changed_hash, 1, 0, "default", false),
        Err(RefDataError::Schema(_))
    ));

    value["schema_version"] = serde_json::Value::String(SCHEMA_VERSION.to_string());
    value["unexpected"] = serde_json::Value::Bool(true);
    let changed = serde_json::to_vec(&value).expect("encode changed");
    let changed_hash = {
        use sha2::{Digest, Sha256};
        let digest = Sha256::digest(&changed);
        digest
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>()
    };
    assert!(matches!(
        verify_and_parse(&changed, &changed_hash, 1, 0, "default", false),
        Err(RefDataError::Json(_))
    ));
    let _ = hash;
}

#[test]
fn consumer_is_monotonic_and_requires_explicit_rollback() {
    let (bytes, hash) = fixture();
    let v2 = verify_and_parse(&bytes, &hash, 2, 0, "default", false).expect("v2");
    let consumer = Consumer::new();
    consumer.apply(v2, false).expect("apply v2");
    assert_eq!(consumer.version(), Some(2));

    // Same version and hash is idempotent.
    let v2_again = verify_and_parse(&bytes, &hash, 2, 0, "default", false).expect("v2 again");
    consumer.apply(v2_again, false).expect("idempotent apply");

    let v1 = verify_and_parse(&bytes, &hash, 1, 0, "default", false).expect("v1 bytes");
    assert!(matches!(
        consumer.apply(v1.clone(), false),
        Err(RefDataError::StaleVersion { .. })
    ));
    assert!(matches!(
        consumer.apply(v1.clone(), true),
        Err(RefDataError::RollbackNotExplicit)
    ));
    consumer
        .apply(v1.mark_rollback(true), true)
        .expect("explicit rollback");
    assert_eq!(consumer.version(), Some(1));
}

#[test]
fn consumer_rejects_version_hash_conflict() {
    let (bytes, hash) = fixture();
    let v1 = verify_and_parse(&bytes, &hash, 1, 0, "default", false).expect("v1");
    let consumer = Consumer::new();
    consumer.apply(v1, false).expect("apply v1");

    let mut value: serde_json::Value = serde_json::from_slice(&bytes).expect("parse value");
    value["assets"][0]["display_name"] = serde_json::Value::String("Conflicting".to_string());
    let changed = serde_json::to_vec(&value).expect("encode changed");
    let changed_hash = {
        use sha2::{Digest, Sha256};
        let digest = Sha256::digest(&changed);
        digest
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>()
    };
    let conflicting = verify_and_parse(&changed, &changed_hash, 1, 0, "default", false)
        .expect("conflicting document is still valid");
    assert!(matches!(
        consumer.apply(conflicting, false),
        Err(RefDataError::VersionHashConflict { version: 1 })
    ));
}

#[test]
fn snapshot_accessors_return_received_metadata() {
    let (bytes, hash) = fixture();
    let snapshot =
        verify_and_parse(&bytes, &hash, 42, 1_700_000_000, "shard-a", true).expect("snapshot");
    assert_eq!(snapshot.version(), 42);
    assert_eq!(snapshot.effective_at_unix(), 1_700_000_000);
    assert_eq!(snapshot.shard_id(), "shard-a");
    assert!(snapshot.is_rollback());
    assert_eq!(snapshot.content_hash(), hash);
    assert_eq!(snapshot.canonical_bytes(), bytes.as_slice());
}

#[test]
fn snapshot_type_is_send_and_sync() {
    fn assert_send_sync<T: Send + Sync>() {}
    assert_send_sync::<Snapshot>();
    assert_send_sync::<Consumer>();
}
