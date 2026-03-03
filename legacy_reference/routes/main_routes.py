"""
Server Money - メイン画面ルート

このファイルは、メイン画面の表示を担当するルートを定義します。
"""

from flask import Blueprint, render_template
from auth import login_required

# Blueprintの作成
main_bp = Blueprint('main', __name__)

@main_bp.route("/")
@login_required
def hello_world():
    """メイン画面の表示"""
    return render_template('index.html')