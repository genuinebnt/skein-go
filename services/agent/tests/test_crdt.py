"""The Python sequence port must agree with the Go reference.

The shared conformance fixtures and the codec's golden bytes are the
contract; a randomised test compares every local edit against a plain
string (the oracle) and checks invariants after every operation.
"""

from __future__ import annotations

import json
import os
import random
from pathlib import Path

import pytest

from skein_agent.domain.crdt import (
    ID,
    ROOT,
    CrdtError,
    CrdtErrorKind,
    Op,
    OpKind,
    Sequence,
    Side,
    decode_ops,
    encode_ops,
)

FIXTURES = Path(__file__).resolve().parents[3] / "testdata" / "conformance" / "crdt"


def _sync(src: Sequence, dst: Sequence) -> None:
    for op in src.ops_since(dst.version()):
        dst.apply(op)
        dst.check_invariants()


def _fixture_files() -> list[Path]:
    return sorted(FIXTURES.glob("*.json"))


@pytest.mark.parametrize("path", _fixture_files(), ids=lambda p: p.name)
def test_conformance_fixture(path: Path) -> None:
    case = json.loads(path.read_text())
    replicas = {name: Sequence(rid) for name, rid in case["replicas"].items()}
    names = sorted(replicas)

    def check(expect: dict[str, str]) -> None:
        for name, want in expect.items():
            targets = names if name == "all" else [name]
            for t in targets:
                assert replicas[t].text() == want, f"{path.name}: replica {t}"

    for step in case["steps"]:
        if "insert" in step:
            seq = replicas[step["replica"]]
            seq.insert(step["insert"]["index"], step["insert"]["text"])
            seq.check_invariants()
        elif "delete" in step:
            seq = replicas[step["replica"]]
            seq.delete(step["delete"]["index"], step["delete"]["count"])
            seq.check_invariants()
        elif "sync" in step:
            if step["sync"] == "all":
                for receiver in names:
                    for sender in names:
                        if sender != receiver:
                            _sync(replicas[sender], replicas[receiver])
            else:
                _sync(replicas[step["sync"]["from"]], replicas[step["sync"]["to"]])
        elif "expect" in step:
            check(step["expect"])
    check(case["expect"])


def test_fixtures_are_present() -> None:
    assert len(_fixture_files()) >= 7


def test_codec_matches_go_golden_bytes() -> None:
    ops = [
        Op(ID(1, 1), 1, OpKind.INSERT, ROOT, Side.RIGHT, ord("h")),
        Op(ID(300, 2), 5, OpKind.DELETE, target=ID(1, 1)),
    ]
    want = bytes.fromhex("534B0102010101010000016802AC0202050101")
    assert encode_ops(ops) == want
    assert decode_ops(want) == ops


@pytest.mark.parametrize(
    "data,why",
    [
        (b"", "empty"),
        (b"XK\x01\x00", "wrong magic"),
        (b"SK\x02\x00", "unknown version"),
        (b"SK\x01\x05\x01\x01\x01", "count larger than the bytes can hold"),
        (bytes.fromhex("534B0101018100010101000168"), "non-minimal varint"),
        (bytes.fromhex("534B0101010101010000016800"), "trailing byte"),
        (bytes.fromhex("534B010103010101000001680000"), "unknown kind"),
        (bytes.fromhex("534B01010101010100000068"), "insert left of root"),
    ],
)
def test_decoder_rejects_malformed_batches(data: bytes, why: str) -> None:
    with pytest.raises(CrdtError) as err:
        decode_ops(data)
    assert err.value.kind in (CrdtErrorKind.CORRUPT, CrdtErrorKind.INVALID_OP), why


def test_reapplying_an_identical_op_is_a_noop_and_a_different_one_conflicts() -> None:
    a = Sequence(1)
    (op,) = a.insert(0, "x")
    a.apply(op)
    a.check_invariants()
    with pytest.raises(CrdtError) as err:
        a.apply(Op(op.id, op.lamport, OpKind.INSERT, ROOT, Side.RIGHT, ord("y")))
    assert err.value.kind is CrdtErrorKind.ID_CONFLICT


def test_op_from_the_future_is_not_ready_and_changes_nothing() -> None:
    a = Sequence(1)
    with pytest.raises(CrdtError) as err:
        a.apply(Op(ID(2, 2), 1, OpKind.INSERT, ROOT, Side.RIGHT, ord("x")))
    assert err.value.kind is CrdtErrorKind.NOT_READY
    assert a.text() == "" and a.version() == {}
    a.check_invariants()


def test_random_local_edits_match_a_string_oracle_and_converge() -> None:
    seed = int(os.environ.get("SKEIN_SEED", random.randrange(1 << 32)))
    print(f"SKEIN_SEED={seed}")
    rng = random.Random(seed)
    replicas = [Sequence(rid) for rid in (7, 3, 11)]
    oracles = ["", "", ""]
    alphabet = "abcé漢🙂\n "
    for _ in range(300):
        i = rng.randrange(3)
        seq = replicas[i]
        if oracles[i] and rng.random() < 0.35:
            idx = rng.randrange(len(oracles[i]))
            count = rng.randint(1, min(3, len(oracles[i]) - idx))
            seq.delete(idx, count)
            oracles[i] = oracles[i][:idx] + oracles[i][idx + count :]
        else:
            idx = rng.randint(0, len(oracles[i]))
            text = "".join(rng.choice(alphabet) for _ in range(rng.randint(1, 3)))
            seq.insert(idx, text)
            oracles[i] = oracles[i][:idx] + text + oracles[i][idx:]
        seq.check_invariants()
        assert seq.text() == oracles[i], f"seed {seed}"
        if rng.random() < 0.2:
            a, b = rng.sample(range(3), 2)
            _sync(replicas[a], replicas[b])
            oracles[b] = replicas[b].text()
    for a in range(3):
        for b in range(3):
            if a != b:
                _sync(replicas[a], replicas[b])
    texts = {s.text() for s in replicas}
    assert len(texts) == 1, f"replicas diverged with seed {seed}"
    batch = replicas[0].ops_since({})
    assert decode_ops(encode_ops(batch)) == batch
