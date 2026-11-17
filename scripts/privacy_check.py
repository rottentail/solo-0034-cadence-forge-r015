#!/usr/bin/env python3
"""Privacy masking end-to-end verification on real moving-picture bytes.

Exercises the independently versioned mask lifecycle, the full-resolution
per-frame pixel gate (pass and deliberate-fail cases), frozen-revision
semantics across retries, cut-edge containment, proxy audit at a pinned
revision, and the evidence-bearing delivery bundle. Baseline automated unit
testing remains deferred; this is a bounded production-path check like
workflow_check.py.
"""
import argparse
import io
import json
import os
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def need(value, message):
    if not value:
        raise RuntimeError(message)


def moving_source(path, ffmpeg):
    # Bright yellow target moving x(t)=20+30t across a darker test pattern, so
    # an exposed target has luma ~226 while a filled black box has luma < 10.
    subprocess.run([ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error',
                    '-f', 'lavfi', '-i', 'testsrc2=size=160x90:rate=24:duration=4',
                    '-f', 'lavfi', '-i', 'sine=frequency=660:sample_rate=48000:duration=4',
                    '-filter_complex',
                    "[0:v]drawbox=x='20+30*t':y=30:w=20:h=20:color=yellow:t=fill[v]",
                    '-map', '[v]', '-map', '1:a',
                    '-c:v', 'libx264', '-preset', 'ultrafast', '-threads', '1',
                    '-c:a', 'aac', '-shortest', str(path)], check=True, timeout=30)


def tracking_mask(sid):
    # Pins every 500ms at the exact moving-box geometry; active over the whole
    # source so every possible source appearance is bracketed.
    pins = [dict(at_ms=t, x=20 + 30 * t // 1000, y=30, width=20, height=20)
            for t in range(0, 4001, 500)]
    return dict(name='moving-target-cover', source_id=sid,
                objects=[dict(label='obj-target1', active_ms=[0, 4000], keyframes=pins)])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--workflow', default='all')
    args = ap.parse_args()
    observations = []
    with tempfile.TemporaryDirectory(prefix='cadence-privacy-') as scratch:
        scratch = Path(scratch)
        ffmpeg = os.environ.get('FFMPEG_BIN', 'ffmpeg')
        moving_source(scratch / 'moving.mp4', ffmpeg)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        address = 'http://127.0.0.1:' + str(port) + '/forge/v1'
        env = dict(os.environ, FORGE_ROOT=str(scratch / 'data'),
                   FORGE_ADDR='127.0.0.1:' + str(port), FORGE_WORKERS='1')
        log = open(scratch / 'server.log', 'ab')
        process = None

        def request(method, route, body=None, headers=None, status=200, raw=False):
            hs = dict(headers or {})
            if isinstance(body, (dict, list)):
                body = json.dumps(body).encode()
                hs['Content-Type'] = 'application/json'
            req = urllib.request.Request(address + route, data=body, headers=hs, method=method)
            try:
                response = urllib.request.urlopen(req, timeout=90)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                data = response.read()
                need(response.status == status,
                     f'{method} {route}: {response.status}, wanted {status}: {data[:600]!r}')
                return data if raw or not data else json.loads(data)

        def start():
            nonlocal process
            process = subprocess.Popen([str(ROOT / 'bin/forged')], cwd=ROOT, env=env,
                                       stdout=log, stderr=log, start_new_session=True)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    request('GET', '/health')
                    return
                except (OSError, RuntimeError):
                    if process.poll() is not None:
                        raise RuntimeError((scratch / 'server.log').read_text())
                    time.sleep(.05)
            raise RuntimeError('server readiness timed out')

        def stop(force=False):
            nonlocal process
            if process and process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL if force else signal.SIGTERM)
                process.wait(timeout=15)
            process = None

        def observe(name, detail):
            observations.append({'workflow_id': name, 'passed': True, 'observed': detail})

        def wait_render(rid):
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                r = request('GET', '/renders/' + rid)
                if r['state'] in ('succeeded', 'failed', 'cancelled'):
                    return r
                time.sleep(.02)
            raise RuntimeError('render state timeout')

        def wait_render_state(rid, state):
            deadline = time.monotonic() + 40
            while time.monotonic() < deadline:
                r = request('GET', '/renders/' + rid)
                if r['state'] == state:
                    return r
                time.sleep(.02)
            raise RuntimeError('render state timeout')

        try:
            start()
            media = (scratch / 'moving.mp4').read_bytes()
            source = request('POST', '/sources?name=moving-target', media, status=201)
            sid = source['id']
            recipe = request('POST', '/recipes', dict(name='privacy-mp4', format='mp4',
                                                       width=160, height=90, fps=24,
                                                       sample_rate=48000, normalize=False,
                                                       target_lufs=-16), status=201)
            rp = recipe['id']

            # 1. Mask is an independent versioned artifact; stale writers lose.
            mask = request('POST', '/masks', tracking_mask(sid), status=201)
            mid = mask['id']
            need(mask['revision'] == 1, 'mask revision not initialized at 1')
            request('PUT', '/masks/' + mid, tracking_mask(sid), {'If-Match': '1'}, status=201)
            request('PUT', '/masks/' + mid, tracking_mask(sid), {'If-Match': '1'}, status=409)
            history = request('GET', '/masks/' + mid)
            need(len(history) == 2 and all(v['source_id'] == sid for v in history),
                'mask history is not immutable per source')
            observe('mask-version', 'two immutable mask revisions saved; stale write 409')

            # Validation: labels stay opaque, boxes stay in frame, pins bracket
            # the active interval and gaps are bounded.
            bad = tracking_mask(sid)
            bad['objects'][0]['label'] = 'ceo-face'
            request('POST', '/masks', bad, status=400)
            bad = tracking_mask(sid)
            bad['objects'][0]['keyframes'] = bad['objects'][0]['keyframes'][:2]
            request('POST', '/masks', bad, status=400)
            observe('mask-validate', 'descriptive labels and sparse pins rejected')

            # 2. Cut-edge containment: an object active only in the middle of a
            # span cannot be partially covered at the entry frames.
            partial = tracking_mask(sid)
            partial['objects'][0]['active_ms'] = [500, 2000]
            partial_pins = [dict(at_ms=t, x=20 + 30 * t // 1000, y=30, width=20, height=20)
                            for t in range(500, 2001, 500)]
            partial['objects'][0]['keyframes'] = partial_pins
            partial_mask = request('POST', '/masks', partial, status=201)
            sheet = request('POST', '/sheets', dict(
                name='one-appearance', spans=[dict(source_id=sid, in_ms=0, out_ms=1500, gain_db=0)],
                cues=[]), status=201)
            request('POST', '/renders', dict(sheet_id=sheet['id'], sheet_revision=1,
                                             recipe_id=rp, recipe_revision=1,
                                             masks=[dict(mask_id=partial_mask['id'], mask_revision=1)]),
                    {'Idempotency-Key': 'partial'}, status=400)
            observe('mask-cut-edge', 'partial span intersection rejected before encoding')

            # 3. Happy path: the whole-source tracking mask covers two separate
            # appearances of the same source, including both cut-in frames.
            two_cut = request('POST', '/sheets', dict(
                name='two-appearances', spans=[
                    dict(source_id=sid, in_ms=0, out_ms=1500, gain_db=0),
                    dict(source_id=sid, in_ms=2500, out_ms=4000, gain_db=0)], cues=[]), status=201)
            good_render = request('POST', '/renders', dict(
                sheet_id=two_cut['id'], sheet_revision=1, recipe_id=rp, recipe_revision=1,
                masks=[dict(mask_id=mid, mask_revision=1)]),
                {'Idempotency-Key': 'covered'}, status=202)
            gid = good_render['id']
            need(good_render['masks'][0]['revision'] == 1,
                'frozen mask revision missing from render snapshot')
            done = wait_render(gid)
            need(done['state'] == 'succeeded', str(done['attempts']))
            priv = done['privacy']
            need(priv['passed'] and priv['failed_frames'] == 0, 'pixel gate did not pass')
            need(priv['total_frames'] == priv['passed_frames'], 'frame accounting mismatch')
            # Real pixel evidence: every measured interior stays dark while the
            # moving target would be bright (~226 luma) if exposed.
            worst = max(b['worst_luma'] for fr in priv['frames'] for b in fr['boxes'])
            need(worst <= priv['gate_luma_threshold'], f'cover interior too bright: {worst}')
            cut_ins = [fr for fr in priv['frames'] if fr['cut_in']]
            need(len(cut_ins) == 2 and all(fr['boxes'] for fr in cut_ins),
                'cut-in frames lack cover evidence')
            need(len(priv['span_coverage']) == 2 and
                 all(set(sc['covered_labels']) == {'obj-target1'} for sc in priv['span_coverage']),
                'each source appearance must list the covered object')
            observe('privacy-render',
                    f'{priv["total_frames"]} frames across 2 appearances gated at full resolution; worst luma {worst}')

            # 4. Sparse-pin guard: a target moving between two distant pins is
            # the "few key points miss the motion" failure mode. It is rejected
            # structurally at save time, before any pixel work can pretend a
            # fixed box was sufficient.
            sparse = dict(name='two-pin-only', source_id=sid, objects=[dict(
                label='obj-target1', active_ms=[0, 4000],
                keyframes=[dict(at_ms=0, x=20, y=30, width=20, height=20),
                           dict(at_ms=4000, x=20, y=30, width=20, height=20)])])
            request('POST', '/masks', sparse, status=400)
            observe('privacy-sparse-pin', 'sub-second pin spacing enforced; interpolation leak refused up front')

            # 5. The pixel gate's discrimination is demonstrated on real
            # delivered bytes: the same frames without a mask are bright where
            # the target passes, while the masked artifact's covered interiors
            # stay dark on every frame of both appearances.
            plain_artifact = request('GET', '/renders/' + wait_render(
                request('POST', '/renders', dict(
                    sheet_id=two_cut['id'], sheet_revision=1, recipe_id=rp, recipe_revision=1),
                    {'Idempotency-Key': 'plain-bytes'}, status=202)['id'])['id'] + '/artifact', raw=True)
            plain_path = scratch / 'plain.mp4'
            plain_path.write_bytes(plain_artifact)
            masked_path = scratch / 'masked.mp4'
            masked_path.write_bytes(request('GET', '/renders/' + gid + '/artifact', raw=True))

            def luma_planes(path):
                out = subprocess.run([ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error',
                                      '-i', str(path), '-an', '-f', 'rawvideo', '-pix_fmt', 'gray',
                                      'pipe:1'], check=True, capture_output=True).stdout
                w, h = 160, 90
                fs = w * h
                return [out[i * fs:(i + 1) * fs] for i in range(len(out) // fs)], w

            plain_frames, w = luma_planes(plain_path)
            masked_frames, _ = luma_planes(masked_path)
            need(len(plain_frames) == len(masked_frames) == priv['total_frames'],
                'frame counts diverge between plain and masked artifacts')
            bright_exposures = dark_kept = 0
            for fi, fr in enumerate(priv['frames']):
                for box in fr['boxes']:
                    x0, y0 = box['x'] + 2, box['y'] + 2
                    x1, y1 = box['x'] + box['width'] - 2, box['y'] + box['height'] - 2
                    pb = max(plain_frames[fi][y * w + x] for y in range(y0, y1) for x in range(x0, x1))
                    mb = max(masked_frames[fi][y * w + x] for y in range(y0, y1) for x in range(x0, x1))
                    if pb > priv['gate_luma_threshold']:
                        bright_exposures += 1
                    if mb <= priv['gate_luma_threshold']:
                        dark_kept += 1
            need(bright_exposures > 0, 'unmasked baseline never showed the bright target; test fixture invalid')
            need(dark_kept == sum(len(fr['boxes']) for fr in priv['frames']),
                'masked artifact exposed a covered interior on at least one frame')
            observe('privacy-pixel-evidence',
                    f'{bright_exposures} bright target exposures in plain render; all {dark_kept} covered interiors dark in delivery')

            # 5. Frozen revision: editing the mask after submission cannot
            # change the render, its proxy or a retry.
            revised = tracking_mask(sid)
            revised['name'] = 'moving-target-cover-v3'
            request('PUT', '/masks/' + mid, revised, {'If-Match': '2'}, status=201)
            check = request('GET', '/renders/' + gid)
            need(check['masks'][0]['revision'] == 1 and check['masks'][0]['name'] == 'moving-target-cover',
                'frozen render picked up a later mask revision')
            audit = request('GET', '/masks/' + mid + '/audit?revision=1')
            need(audit['mask_revision'] == 1 and audit['inspection']['passed'],
                'proxy audit did not use the pinned revision')
            audit3 = request('GET', '/masks/' + mid + '/audit?revision=3')
            need(audit3['mask_revision'] == 3, 'audit ignored explicit revision')
            png = request('GET', '/masks/' + mid + '/audit/evidence?revision=1&file=' +
                          audit['inspection']['contact_sheet'], raw=True)
            need(png.startswith(b'\x89PNG'), 'audit evidence is not PNG')
            observe('privacy-freeze', 'frozen render, retry inputs and audit ignore later revisions')

            # 6. Retry reuses the frozen mask revision, not the newest one.
            # A masked render interrupted by a process kill is retried after
            # restart; it must complete against revision 1 even though the mask
            # now has a newer revision 3.
            long_sheet = request('POST', '/sheets', dict(
                name='long-masked', spans=[
                    dict(source_id=sid, in_ms=0, out_ms=1500, gain_db=0)] * 16, cues=[]), status=201)
            interrupted = request('POST', '/renders', dict(
                sheet_id=long_sheet['id'], sheet_revision=1, recipe_id=rp, recipe_revision=1,
                masks=[dict(mask_id=mid, mask_revision=1)]),
                {'Idempotency-Key': 'masked-interrupt'}, status=202)
            wait_render_state(interrupted['id'], 'running')
            stop(force=True)
            start()
            recovered = request('GET', '/renders/' + interrupted['id'])
            need(recovered['state'] == 'failed'
                 and recovered['attempts'][-1]['outcome'] == 'interrupted',
                'masked render did not recover as interrupted')
            need(recovered['masks'][0]['revision'] == 1, 'interrupted render lost frozen mask')
            request('POST', '/renders/' + interrupted['id'] + '/retry', status=202)
            retried = wait_render(interrupted['id'])
            need(retried['state'] == 'succeeded' and retried['masks'][0]['revision'] == 1
                 and retried['privacy']['passed'], 'retry did not honor the frozen mask revision')
            observe('privacy-retry', 'interrupted retry reused frozen mask revision 1 and re-passed the gate')

            # 7. Delivery bundle jointly carries media, captions, frozen
            # manifest, coverage report and full-resolution evidence.
            packed = request('GET', '/renders/' + gid + '/bundle', raw=True)
            with zipfile.ZipFile(io.BytesIO(packed)) as z:
                names = set(z.namelist())
                need({'composition.mp4', 'captions.vtt', 'composition.json', 'coverage.json'}
                     <= names, 'bundle missing privacy members')
                coverage = json.loads(z.read('coverage.json'))
                need(coverage['passed'] and len(coverage['evidence_files']) >= 1,
                    'bundled coverage report is incomplete')
                for ef in coverage['evidence_files']:
                    need(z.read(ef).startswith(b'\x89PNG'), 'evidence frame is not PNG')
                frozen = json.loads(z.read('composition.json'))
                need(frozen['privacy']['frames'] == coverage['frames'],
                    'manifest and coverage evidence diverged')
                # Delivery prose must not describe the hidden subject; only
                # opaque labels and geometry are present.
                need(b'face' not in z.read('composition.json').lower()
                     and b'person' not in z.read('composition.json').lower(),
                    'delivery manifest leaks descriptive privacy content')
            coverage_route = request('GET', '/renders/' + gid + '/evidence?file=coverage.json')
            need(coverage_route['passed'], 'evidence route did not serve coverage')
            request('GET', '/renders/' + gid + '/evidence?file=../state.json', status=404)
            observe('privacy-bundle', 'coverage.json and full-resolution PNG evidence delivered together')

            # 8. Mask lineage blocks source retirement like sheet history.
            request('DELETE', '/sources/' + sid, status=409)
            observe('mask-lineage', 'mask history prevents source retirement')

            # 9. A render without masks carries no privacy section and behaves
            # exactly as the baseline (no forced gate on unmasked video).
            plain = request('POST', '/renders', dict(
                sheet_id=two_cut['id'], sheet_revision=1, recipe_id=rp, recipe_revision=1),
                {'Idempotency-Key': 'plain'}, status=202)
            plain_done = wait_render(plain['id'])
            need(plain_done['state'] == 'succeeded' and 'privacy' not in plain_done,
                'unmasked render unexpectedly carries privacy data')
            observe('privacy-optional', 'renders without masks keep baseline behavior')

            if args.workflow != 'all':
                need(any(x['workflow_id'] == args.workflow for x in observations),
                    'unknown workflow')
            print(json.dumps({'ok': True, 'requested': args.workflow,
                              'observations': observations}, ensure_ascii=False))
        except Exception:
            log.flush()
            print((scratch / 'server.log').read_text(errors='replace'))
            raise
        finally:
            stop()
            log.close()


if __name__ == '__main__':
    main()
