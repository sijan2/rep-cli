import importlib.util
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest import mock

path = Path(__file__).resolve().parents[1] / 'skills/rep-browser-network/scripts/rep_arc_download.py'
spec = importlib.util.spec_from_file_location('rep_downloader', path)
downloader = importlib.util.module_from_spec(spec)
spec.loader.exec_module(downloader)


class DownloaderTests(unittest.TestCase):
    def test_delegates_one_download_with_no_browser_setup_or_raw_protocol_calls(self):
        args = SimpleNamespace(url='https://fixture.test/file.pdf', output='fixture.pdf', timeout=120, overwrite=True, rep_bin='/fixture/bin/rep')
        with mock.patch.object(downloader, 'Client') as client:
            client.return_value.call.return_value = {'complete': True, 'meta': {'path': 'fixture.pdf'}}
            result = downloader.download(args)
        self.assertTrue(result['complete'])
        client.assert_called_once_with(inherit_scope=True, binary='/fixture/bin/rep')
        client.return_value.call.assert_called_once_with('download', args.url, args.output, '--timeout', '120s', '--overwrite', timeout=130)
