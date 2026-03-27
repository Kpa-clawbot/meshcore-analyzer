#!/usr/bin/env python3
"""
Validate proto definitions against captured Node fixtures.

Parses each .proto file, extracts message field names/types/json_names,
then compares against the actual JSON fixtures to find mismatches.

Usage:
    python tools/validate-protos.py
"""

import json
import os
import re
import sys
from collections import defaultdict

PROTO_DIR = os.path.join(os.path.dirname(__file__), '..', 'proto')
FIXTURE_DIR = os.path.join(PROTO_DIR, 'testdata', 'node-fixtures')

# ─── Proto Parser ───────────────────────────────────────────────────────────────

def parse_proto_file(filepath):
    """Parse a .proto file and return dict of message_name -> { fields, oneofs }."""
    with open(filepath, encoding='utf-8') as f:
        content = f.read()

    messages = {}
    # Remove comments
    content_clean = re.sub(r'//[^\n]*', '', content)

    _parse_messages(content_clean, messages)
    return messages


def _parse_messages(content, messages, prefix=''):
    """Recursively parse message definitions."""
    msg_pattern = re.compile(
        r'message\s+(\w+)\s*\{', re.DOTALL
    )
    pos = 0
    while pos < len(content):
        m = msg_pattern.search(content, pos)
        if not m:
            break
        msg_name = m.group(1)
        full_name = f'{prefix}{msg_name}' if prefix else msg_name
        brace_start = m.end() - 1
        brace_end = _find_matching_brace(content, brace_start)
        if brace_end == -1:
            break
        body = content[brace_start + 1:brace_end]

        fields = _parse_fields(body)
        messages[full_name] = fields

        # Parse nested messages
        _parse_messages(body, messages, prefix=f'{full_name}.')

        pos = brace_end + 1


def _find_matching_brace(content, start):
    """Find the closing brace matching the opening brace at start."""
    depth = 0
    for i in range(start, len(content)):
        if content[i] == '{':
            depth += 1
        elif content[i] == '}':
            depth -= 1
            if depth == 0:
                return i
    return -1


def _parse_fields(body):
    """Parse fields from a message body."""
    fields = {}

    # Handle oneof blocks - extract their fields
    oneof_pattern = re.compile(r'oneof\s+\w+\s*\{([^}]*)\}', re.DOTALL)
    oneof_fields = []
    for om in oneof_pattern.finditer(body):
        oneof_body = om.group(1)
        for fm in _field_pattern().finditer(oneof_body):
            oneof_fields.append(fm)

    # Remove oneof blocks and nested message blocks from body for regular field parsing
    body_no_oneof = oneof_pattern.sub('', body)
    body_no_nested = _remove_nested_messages(body_no_oneof)

    for fm in _field_pattern().finditer(body_no_nested):
        _add_field(fm, fields)

    # Add oneof fields
    for fm in oneof_fields:
        _add_field(fm, fields, is_oneof=True)

    # Handle map fields
    map_pattern = re.compile(
        r'map\s*<\s*(\w+)\s*,\s*(\w+)\s*>\s+(\w+)\s*=\s*\d+'
        r'(?:\s*\[json_name\s*=\s*"([^"]+)"\])?\s*;'
    )
    for mm in map_pattern.finditer(body_no_nested):
        key_type = mm.group(1)
        val_type = mm.group(2)
        field_name = mm.group(3)
        json_name = mm.group(4) or field_name
        fields[json_name] = {
            'proto_name': field_name,
            'proto_type': f'map<{key_type},{val_type}>',
            'repeated': False,
            'optional': False,
            'is_map': True,
        }

    return fields


def _field_pattern():
    return re.compile(
        r'(repeated\s+|optional\s+)?'
        r'(\w+)\s+'
        r'(\w+)\s*=\s*\d+'
        r'(?:\s*\[([^\]]*)\])?\s*;'
    )


def _add_field(match, fields, is_oneof=False):
    modifier = (match.group(1) or '').strip()
    proto_type = match.group(2)
    field_name = match.group(3)
    options = match.group(4) or ''

    json_name = field_name
    jn_match = re.search(r'json_name\s*=\s*"([^"]+)"', options)
    if jn_match:
        json_name = jn_match.group(1)

    fields[json_name] = {
        'proto_name': field_name,
        'proto_type': proto_type,
        'repeated': modifier == 'repeated',
        'optional': modifier == 'optional' or is_oneof,
        'is_map': False,
    }


def _remove_nested_messages(body):
    """Remove nested message/enum blocks from body."""
    result = []
    depth = 0
    in_nested = False
    i = 0
    # Find 'message X {' or 'enum X {' patterns
    nested_start = re.compile(r'(?:message|enum)\s+\w+\s*\{')
    while i < len(body):
        if not in_nested:
            m = nested_start.search(body, i)
            if m and m.start() == i:
                in_nested = True
                depth = 1
                i = m.end()
                continue
            elif m:
                result.append(body[i:m.start()])
                in_nested = True
                depth = 1
                i = m.end()
                continue
            else:
                result.append(body[i:])
                break
        else:
            if body[i] == '{':
                depth += 1
            elif body[i] == '}':
                depth -= 1
                if depth == 0:
                    in_nested = False
            i += 1
    return ''.join(result)


# ─── Type Checking ──────────────────────────────────────────────────────────────

PROTO_SCALAR_TYPES = {
    'int32': (int, float),
    'int64': (int, float),
    'uint32': (int, float),
    'uint64': (int, float),
    'sint32': (int, float),
    'sint64': (int, float),
    'fixed32': (int, float),
    'fixed64': (int, float),
    'sfixed32': (int, float),
    'sfixed64': (int, float),
    'float': (int, float),
    'double': (int, float),
    'bool': (bool,),
    'string': (str,),
    'bytes': (str,),  # base64-encoded in JSON
}


def check_type_match(proto_type, json_value):
    """Check if a JSON value matches the expected proto type.
    Returns (matches, detail_string)."""
    if json_value is None:
        return True, 'null (optional)'

    if proto_type in PROTO_SCALAR_TYPES:
        expected = PROTO_SCALAR_TYPES[proto_type]
        actual_type = type(json_value).__name__
        if isinstance(json_value, expected):
            return True, f'{actual_type} matches {proto_type}'
        # Special: int is valid for float/double
        if proto_type in ('float', 'double') and isinstance(json_value, int):
            return True, f'int is valid for {proto_type}'
        return False, f'expected {proto_type} but got {actual_type}'

    # Message type - should be a dict
    if isinstance(json_value, dict):
        return True, f'object (message {proto_type})'
    if isinstance(json_value, list):
        return True, f'array (repeated {proto_type})'

    return False, f'expected message {proto_type} but got {type(json_value).__name__}'


# ─── Fixture→Message Mapping ───────────────────────────────────────────────────

FIXTURE_TO_MESSAGE = {
    'stats.json': ('StatsResponse', 'object'),
    'health.json': ('HealthResponse', 'object'),
    'perf.json': ('PerfResponse', 'object'),
    'nodes.json': ('NodeListResponse', 'object'),
    'node-detail.json': ('NodeDetailResponse', 'object'),
    'node-health.json': ('NodeHealthResponse', 'object'),
    'node-search.json': ('NodeSearchResponse', 'object'),
    'node-paths.json': ('NodePathsResponse', 'object'),
    'node-analytics.json': ('NodeAnalyticsResponse', 'object'),
    'bulk-health.json': ('BulkHealthEntry', 'array'),
    'observers.json': ('ObserverListResponse', 'object'),
    'observer-detail.json': ('ObserverDetailResponse', 'object'),
    'observer-analytics.json': ('ObserverAnalyticsResponse', 'object'),
    'packets.json': ('PacketListResponse', 'object'),
    'packets-grouped.json': ('GroupedPacketListResponse', 'object'),
    'packets-since.json': ('GroupedPacketListResponse', 'object'),
    'packet-detail.json': ('PacketDetailResponse', 'object'),
    'packet-timestamps.json': ('PacketTimestampsResponse', 'bare-array'),
    'channels.json': ('ChannelListResponse', 'object'),
    'channel-messages.json': ('ChannelMessagesResponse', 'object'),
    'analytics-rf.json': ('RFAnalyticsResponse', 'object'),
    'analytics-topology.json': ('TopologyResponse', 'object'),
    'analytics-channels.json': ('ChannelAnalyticsResponse', 'object'),
    'analytics-hash-sizes.json': ('HashSizeAnalyticsResponse', 'object'),
    'analytics-distance.json': ('DistanceAnalyticsResponse', 'object'),
    'analytics-subpaths.json': ('SubpathsResponse', 'object'),
    'config-theme.json': ('ThemeResponse', 'object'),
    'config-regions.json': ('RegionsResponse', 'bare-map'),
    'config-client.json': ('ClientConfigResponse', 'object'),
    'config-cache.json': ('CacheConfigResponse', 'object'),
    'config-map.json': ('MapConfigResponse', 'object'),
    'iata-coords.json': ('IataCoordsResponse', 'object'),
    'websocket-message.json': ('WSMessage', 'object'),
}

# Sub-message field mappings for recursive validation
FIELD_TYPE_TO_MESSAGE = {
    # stats.proto
    'MemoryStats': 'MemoryStats',
    'EventLoopStats': 'EventLoopStats',
    'CacheStats': 'CacheStats',
    'WebSocketStats': 'WebSocketStats',
    'HealthPacketStoreStats': 'HealthPacketStoreStats',
    'HealthPerfStats': 'HealthPerfStats',
    'SlowQuery': 'SlowQuery',
    'EndpointStats': 'EndpointStats',
    'PerfCacheStats': 'PerfCacheStats',
    'PerfPacketStoreStats': 'PerfPacketStoreStats',
    'PacketStoreIndexes': 'PacketStoreIndexes',
    'SqliteStats': 'SqliteStats',
    'SqliteRowCounts': 'SqliteRowCounts',
    'WalPages': 'WalPages',
    # common.proto
    'RoleCounts': 'RoleCounts',
    'SignalStats': 'SignalStats',
    'Histogram': 'Histogram',
    'HistogramBin': 'HistogramBin',
    'TimeBucket': 'TimeBucket',
    # node.proto
    'Node': 'Node',
    'NodeObserverStats': 'NodeObserverStats',
    'NodeStats': 'NodeStats',
    'PathHop': 'PathHop',
    'PathEntry': 'PathEntry',
    'TimeRange': 'TimeRange',
    'SnrTrendEntry': 'SnrTrendEntry',
    'PayloadTypeCount': 'PayloadTypeCount',
    'HopDistEntry': 'HopDistEntry',
    'PeerInteraction': 'PeerInteraction',
    'HeatmapCell': 'HeatmapCell',
    'ComputedNodeStats': 'ComputedNodeStats',
    # observer.proto
    'Observer': 'Observer',
    'SnrDistributionEntry': 'SnrDistributionEntry',
    # packet.proto
    'Transmission': 'Transmission',
    'Observation': 'Observation',
    'GroupedPacket': 'GroupedPacket',
    'ByteRange': 'ByteRange',
    'PacketBreakdown': 'PacketBreakdown',
    # decoded.proto
    'DecodedResult': 'DecodedResult',
    'DecodedHeader': 'DecodedHeader',
    'DecodedPath': 'DecodedPath',
    'DecodedPayload': 'DecodedPayload',
    'DecodedFlatPayload': 'DecodedFlatPayload',
    'DecodedTransportCodes': 'DecodedTransportCodes',
    'AdvertFlags': 'AdvertFlags',
    'AdvertPayload': 'AdvertPayload',
    # channel.proto
    'Channel': 'Channel',
    'ChannelMessage': 'ChannelMessage',
    # analytics.proto
    'PayloadTypeSignal': 'PayloadTypeSignal',
    'SignalOverTimeEntry': 'SignalOverTimeEntry',
    'ScatterPoint': 'ScatterPoint',
    'PayloadTypeEntry': 'PayloadTypeEntry',
    'HourlyCount': 'HourlyCount',
    'TopologyHopDist': 'TopologyHopDist',
    'TopRepeater': 'TopRepeater',
    'TopPair': 'TopPair',
    'HopsVsSnr': 'HopsVsSnr',
    'ObserverRef': 'ObserverRef',
    'ObserverReach': 'ObserverReach',
    'ReachRing': 'ReachRing',
    'ReachNode': 'ReachNode',
    'MultiObsObserver': 'MultiObsObserver',
    'MultiObsNode': 'MultiObsNode',
    'BestPathEntry': 'BestPathEntry',
    'ChannelAnalyticsSummary': 'ChannelAnalyticsSummary',
    'TopSender': 'TopSender',
    'ChannelTimelineEntry': 'ChannelTimelineEntry',
    'DistanceSummary': 'DistanceSummary',
    'DistanceHop': 'DistanceHop',
    'DistancePath': 'DistancePath',
    'DistancePathHop': 'DistancePathHop',
    'CategoryDistStats': 'CategoryDistStats',
    'DistOverTimeEntry': 'DistOverTimeEntry',
    'HashSizeHourly': 'HashSizeHourly',
    'HashSizeHop': 'HashSizeHop',
    'MultiByteNode': 'MultiByteNode',
    'Subpath': 'Subpath',
    # websocket.proto
    'WSPacketData': 'WSPacketData',
}


# ─── Validator ──────────────────────────────────────────────────────────────────

class Mismatch:
    def __init__(self, fixture, path, severity, message):
        self.fixture = fixture
        self.path = path
        self.severity = severity  # 'ERROR' or 'WARNING'
        self.message = message

    def __str__(self):
        return f'  [{self.severity}] {self.path}: {self.message}'


def validate_object(fixture_name, data, message_name, all_messages, path='',
                     mismatches=None):
    """Validate a JSON object against a proto message definition."""
    if mismatches is None:
        mismatches = []

    if not isinstance(data, dict):
        mismatches.append(Mismatch(
            fixture_name, path or message_name, 'ERROR',
            f'Expected object for {message_name}, got {type(data).__name__}'
        ))
        return mismatches

    if message_name not in all_messages:
        mismatches.append(Mismatch(
            fixture_name, path or message_name, 'WARNING',
            f'Message {message_name} not found in parsed protos'
        ))
        return mismatches

    proto_fields = all_messages[message_name]
    current_path = path or message_name

    # Check for fixture fields not in proto
    for json_key in data.keys():
        if json_key.startswith('_'):
            # Underscore-prefixed fields are internal/computed, skip
            continue
        if json_key not in proto_fields:
            mismatches.append(Mismatch(
                fixture_name, f'{current_path}.{json_key}', 'ERROR',
                f'Field "{json_key}" exists in fixture but NOT in proto {message_name}'
            ))

    # Check proto fields against fixture
    for json_key, field_info in proto_fields.items():
        proto_type = field_info['proto_type']
        is_optional = field_info['optional']
        is_repeated = field_info['repeated']
        is_map = field_info['is_map']

        if json_key not in data:
            if is_optional:
                continue  # Optional field absent — OK
            if is_repeated or is_map:
                continue  # Repeated/map fields default to empty — OK
            # Proto3 scalars default to zero-value, so absence is valid
            if proto_type in PROTO_SCALAR_TYPES:
                continue
            # Message fields default to null/absent
            if proto_type not in PROTO_SCALAR_TYPES:
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'WARNING',
                    f'Proto field "{json_key}" ({proto_type}) absent from fixture '
                    f'(may be zero-value default)'
                ))
            continue

        value = data[json_key]

        # Null value
        if value is None:
            if not is_optional:
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'ERROR',
                    f'Field "{json_key}" is null in fixture but NOT optional in proto'
                ))
            continue

        # Map type
        if is_map:
            if not isinstance(value, dict):
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'ERROR',
                    f'Expected map/object for "{json_key}", got {type(value).__name__}'
                ))
            else:
                # Validate map values if they're message types
                val_type = proto_type.split(',')[1].rstrip('>')
                if val_type in FIELD_TYPE_TO_MESSAGE:
                    msg_name = FIELD_TYPE_TO_MESSAGE[val_type]
                    for mk, mv in list(value.items())[:3]:
                        if isinstance(mv, dict):
                            validate_object(
                                fixture_name, mv, msg_name, all_messages,
                                f'{current_path}.{json_key}["{mk[:20]}"]',
                                mismatches
                            )
            continue

        # Repeated type
        if is_repeated:
            if not isinstance(value, list):
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'ERROR',
                    f'Expected array for repeated "{json_key}", got {type(value).__name__}'
                ))
            elif len(value) > 0:
                sample = value[0]
                if proto_type in PROTO_SCALAR_TYPES:
                    ok, detail = check_type_match(proto_type, sample)
                    if not ok:
                        mismatches.append(Mismatch(
                            fixture_name,
                            f'{current_path}.{json_key}[0]', 'ERROR',
                            f'Array element type mismatch: {detail}'
                        ))
                elif proto_type in FIELD_TYPE_TO_MESSAGE:
                    msg_name = FIELD_TYPE_TO_MESSAGE[proto_type]
                    if isinstance(sample, dict):
                        validate_object(
                            fixture_name, sample, msg_name, all_messages,
                            f'{current_path}.{json_key}[0]',
                            mismatches
                        )
            continue

        # Scalar type
        if proto_type in PROTO_SCALAR_TYPES:
            ok, detail = check_type_match(proto_type, value)
            if not ok:
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'ERROR',
                    f'Type mismatch: {detail}'
                ))
            continue

        # Message type
        if proto_type in FIELD_TYPE_TO_MESSAGE:
            msg_name = FIELD_TYPE_TO_MESSAGE[proto_type]
            if isinstance(value, dict):
                validate_object(
                    fixture_name, value, msg_name, all_messages,
                    f'{current_path}.{json_key}',
                    mismatches
                )
            else:
                mismatches.append(Mismatch(
                    fixture_name, f'{current_path}.{json_key}', 'ERROR',
                    f'Expected object for message field {proto_type}, '
                    f'got {type(value).__name__}'
                ))
            continue

    return mismatches


# ─── Main ───────────────────────────────────────────────────────────────────────

def main():
    # Parse all proto files
    all_messages = {}
    proto_files = sorted(f for f in os.listdir(PROTO_DIR) if f.endswith('.proto'))
    for pf in proto_files:
        filepath = os.path.join(PROTO_DIR, pf)
        msgs = parse_proto_file(filepath)
        all_messages.update(msgs)

    print(f'Parsed {len(all_messages)} messages from {len(proto_files)} proto files')
    print(f'Messages: {", ".join(sorted(all_messages.keys()))}')
    print()

    # Load and validate fixtures
    fixture_files = sorted(f for f in os.listdir(FIXTURE_DIR) if f.endswith('.json'))
    all_mismatches = []
    fixtures_checked = 0

    for fixture_file in fixture_files:
        if fixture_file not in FIXTURE_TO_MESSAGE:
            print(f'⚠ No mapping for fixture: {fixture_file} — skipping')
            continue

        message_name, shape = FIXTURE_TO_MESSAGE[fixture_file]
        filepath = os.path.join(FIXTURE_DIR, fixture_file)
        with open(filepath, encoding='utf-8') as f:
            data = json.load(f)

        fixtures_checked += 1
        mismatches = []

        if shape == 'object':
            validate_object(fixture_file, data, message_name, all_messages,
                            mismatches=mismatches)
        elif shape == 'array':
            # Bare array — validate first element against the message
            if isinstance(data, list):
                if len(data) > 0 and isinstance(data[0], dict):
                    validate_object(fixture_file, data[0], message_name,
                                    all_messages, path=f'{message_name}[0]',
                                    mismatches=mismatches)
                # Flag structural note (serialization concern, not a field mismatch)
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'WARNING',
                    f'API returns a bare JSON array, but proto wraps it in a '
                    f'response message. Serialization layer must handle unwrapping.'
                ))
            else:
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'ERROR',
                    f'Expected array for bare-array fixture, got {type(data).__name__}'
                ))
        elif shape == 'bare-array':
            # Bare array of scalars (e.g. packet-timestamps)
            if isinstance(data, list):
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'WARNING',
                    f'API returns a bare JSON array of {len(data)} elements. '
                    f'Proto wraps it in {message_name}. '
                    f'Serialization layer must handle unwrapping.'
                ))
            else:
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'ERROR',
                    f'Expected array, got {type(data).__name__}'
                ))
        elif shape == 'bare-map':
            # Bare JSON object used as a map (e.g. config-regions)
            if isinstance(data, dict):
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'WARNING',
                    f'API returns a bare JSON map with {len(data)} entries. '
                    f'Proto wraps it in {message_name}.regions. '
                    f'Serialization layer must handle unwrapping.'
                ))
            else:
                mismatches.append(Mismatch(
                    fixture_file, message_name, 'ERROR',
                    f'Expected map, got {type(data).__name__}'
                ))

        if mismatches:
            all_mismatches.extend(mismatches)

    # ─── Report ─────────────────────────────────────────────────────────────────

    print('=' * 78)
    print(f'VALIDATION REPORT — {fixtures_checked} fixtures checked')
    print('=' * 78)
    print()

    # Group by fixture
    by_fixture = defaultdict(list)
    for m in all_mismatches:
        by_fixture[m.fixture].append(m)

    error_count = sum(1 for m in all_mismatches if m.severity == 'ERROR')
    warn_count = sum(1 for m in all_mismatches if m.severity == 'WARNING')

    for fixture_file in sorted(by_fixture.keys()):
        fixture_mismatches = by_fixture[fixture_file]
        msg_name = FIXTURE_TO_MESSAGE[fixture_file][0]
        errors = [m for m in fixture_mismatches if m.severity == 'ERROR']
        warnings = [m for m in fixture_mismatches if m.severity == 'WARNING']
        status = '❌' if errors else '⚠' if warnings else '✅'
        print(f'{status} {fixture_file} → {msg_name}')
        for m in fixture_mismatches:
            print(str(m))
        print()

    # List clean fixtures
    clean = [f for f in fixture_files
             if f in FIXTURE_TO_MESSAGE and f not in by_fixture]
    if clean:
        for f in clean:
            msg_name = FIXTURE_TO_MESSAGE[f][0]
            print(f'✅ {f} → {msg_name}')
        print()

    print('─' * 78)
    print(f'Total: {error_count} errors, {warn_count} warnings '
          f'across {len(by_fixture)} fixtures with issues')
    print(f'Clean: {len(clean)} fixtures with no issues')
    print('─' * 78)

    return 1 if error_count > 0 else 0


if __name__ == '__main__':
    sys.exit(main())
